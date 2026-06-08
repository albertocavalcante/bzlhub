package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	gobzlmod "github.com/albertocavalcante/go-bzlmod"
	"github.com/albertocavalcante/go-bzlmod/bazeltools"

	"github.com/albertocavalcante/bzlhub/internal/eventbus"
	"github.com/albertocavalcante/bzlhub/internal/fetch"
	"github.com/albertocavalcante/bzlhub/internal/mirror"
)

// RecursiveOptions configures a closure walk.
type RecursiveOptions struct {
	// Reporter, if non-nil, receives one call per (name, version) processed.
	// Events:
	//   kind="enter"    — beginning fetch
	//   kind="done"     — successfully ingested + mirrored
	//   kind="skip"     — already-seen (name@version)
	//   kind="error"    — fetch/parse failed; details in err
	//
	// Reporter is invoked from a single goroutine (the coordinator), so
	// callers don't need to make it concurrency-safe.
	Reporter func(event RecursiveEvent)

	// Mirror, if non-nil, persists every fetched module as a side effect.
	Mirror *mirror.Writer

	// BazelToolsForVersion, if non-empty, seeds the BFS with Bazel's
	// implicit MODULE.tools deps for the given Bazel version (e.g.,
	// "9.1.0"). Required for a fully self-sufficient air-gap mirror.
	BazelToolsForVersion string

	// Workers caps concurrent upstream fetches. 0 → 8.
	Workers int

	// Bus, if non-nil, receives a module_indexed event per successful
	// "done" outcome (matches the event the canopy service publishes
	// for one-off Bump calls — single shape for both code paths so
	// the UI doesn't need to special-case recursive ingest). Errors
	// and skips are not published to the bus (they're surfaced via
	// Reporter for CLI output).
	Bus *eventbus.Bus
}

// RecursiveEvent is one step in the closure walk, surfaced for progress reporting.
type RecursiveEvent struct {
	Kind    string // enter | done | skip | error
	Module  string
	Version string
	Detail  string
	Err     error
}

// Result summarizes a closure walk.
type Result struct {
	Visited  int
	Mirrored int
	Errors   []RecursiveEvent
}

// RecursiveFromRegistry walks the bazel_dep closure of (rootModule, rootVersion)
// against registryURL. Each unique (name, version) is fetched, optionally
// mirrored, and queued for its own bazel_deps. Missing modules in the
// registry are logged as errors but do not abort the walk.
//
// Concurrent fetch model: one coordinator goroutine maintains the visited
// set and a local pending queue; up to opts.Workers worker goroutines
// pull jobs and report discoveries back via a buffered channel. The
// coordinator uses a select to either dispatch a queued job or absorb
// a discovery, eliminating the classic producer-consumer deadlock that
// happens with naive unbuffered + non-select implementations.
//
// Per-module write serialization (e.g., metadata.json merge) is handled
// inside *mirror.Writer.
func RecursiveFromRegistry(ctx context.Context, registryURL, rootModule, rootVersion string, opts RecursiveOptions) (*Result, error) {
	client := fetch.NewClient()
	workers := opts.Workers
	if workers <= 0 {
		workers = 8
	}

	// Channels.
	//
	// jobs:        coordinator → workers
	// discoveries: workers     → coordinator
	//
	// Buffering at workers*2 absorbs short bursts without forcing the
	// coordinator into select-on-receive while it has nothing to send.
	jobs := make(chan moduleKey, workers*2)
	discoveries := make(chan discovery, workers*2)

	// Start workers.
	var workersWg sync.WaitGroup
	for i := 0; i < workers; i++ {
		workersWg.Add(1)
		go func() {
			defer workersWg.Done()
			for k := range jobs {
				deps, err := walkOne(ctx, client, registryURL, k, opts.Mirror)
				discoveries <- discovery{Key: k, Deps: deps, Err: err}
			}
		}()
	}

	// Coordinator state (this goroutine).
	visited := make(map[string]bool)
	pending := make([]moduleKey, 0, 32)
	res := &Result{}
	inflight := 0

	// enqueue admits a new module to the closure if not already visited.
	// New modules go to `pending`; the select loop below drains pending
	// into `jobs` whenever a worker has capacity.
	enqueue := func(k moduleKey) {
		key := k.name + "@" + k.version
		if visited[key] {
			emit(opts, RecursiveEvent{Kind: "skip", Module: k.name, Version: k.version})
			return
		}
		visited[key] = true
		res.Visited++
		emit(opts, RecursiveEvent{Kind: "enter", Module: k.name, Version: k.version})
		pending = append(pending, k)
		inflight++
	}

	// Seed the closure: root + Bazel MODULE.tools (if requested).
	enqueue(moduleKey{name: rootModule, version: rootVersion})
	if opts.BazelToolsForVersion != "" {
		for _, d := range bazeltools.LookupDeps(opts.BazelToolsForVersion) {
			enqueue(moduleKey{name: d.Name, version: d.Version})
		}
	}

	// Drive the closure until all in-flight work + pending queue are empty.
	for inflight > 0 {
		// If we have pending jobs, race a send against a receive.
		// Otherwise just wait for a discovery.
		if len(pending) > 0 {
			select {
			case jobs <- pending[0]:
				pending = pending[1:]
			case d := <-discoveries:
				inflight--
				handle(opts, res, d, enqueue)
			}
		} else {
			d := <-discoveries
			inflight--
			handle(opts, res, d, enqueue)
		}
	}

	close(jobs)
	workersWg.Wait()
	close(discoveries)
	return res, nil
}

// discovery is what a worker produces for one moduleKey.
type discovery struct {
	Key  moduleKey
	Deps []moduleKey
	Err  error
}

// handle processes one discovery from a worker: records error or success,
// emits the event, and re-enqueues any new deps.
func handle(opts RecursiveOptions, res *Result, d discovery, enqueue func(moduleKey)) {
	if d.Err != nil {
		ev := RecursiveEvent{Kind: "error", Module: d.Key.name, Version: d.Key.version, Err: d.Err}
		res.Errors = append(res.Errors, ev)
		emit(opts, ev)
		return
	}
	res.Mirrored++
	emit(opts, RecursiveEvent{
		Kind:    "done",
		Module:  d.Key.name,
		Version: d.Key.version,
		Detail:  fmt.Sprintf("%d deps", len(d.Deps)),
	})
	if opts.Bus != nil {
		opts.Bus.Publish(eventbus.Event{
			Kind: "module_indexed",
			Data: map[string]any{
				"module":  d.Key.name,
				"version": d.Key.version,
				"source":  "recursive",
			},
		})
	}
	for _, dep := range d.Deps {
		enqueue(dep)
	}
}

type moduleKey struct{ name, version string }

// walkOne fetches one (module, version), mirrors it if requested, and
// returns the parsed bazel_dep list. Safe to call concurrently from
// multiple workers as long as the *mirror.Writer is shared and itself
// concurrency-safe (it is, via a per-module-name mutex on metadata
// writes; blob writes are content-addressed and atomic-rename).
func walkOne(ctx context.Context, client *fetch.Client, registryURL string, k moduleKey, mw *mirror.Writer) ([]moduleKey, error) {
	if mw == nil {
		return depsFromRegistry(ctx, client, registryURL, k)
	}
	return walkOneMirrored(ctx, client, registryURL, k, mw)
}

func depsFromRegistry(ctx context.Context, client *fetch.Client, registryURL string, k moduleKey) ([]moduleKey, error) {
	modBytes, err := client.GetModuleBazel(ctx, registryURL, k.name, k.version)
	if err != nil {
		return nil, err
	}
	return parseDeps(modBytes)
}

func walkOneMirrored(ctx context.Context, client *fetch.Client, registryURL string, k moduleKey, mw *mirror.Writer) ([]moduleKey, error) {
	if err := mw.EnsureRegistryJSON(); err != nil {
		return nil, err
	}

	srcBytes, err := client.GetSourceJSONBytes(ctx, registryURL, k.name, k.version)
	if err != nil {
		return nil, err
	}
	src, err := fetch.ParseSourceJSON(srcBytes)
	if err != nil {
		return nil, err
	}
	if src.URL == "" {
		return nil, errors.New("source.json missing url")
	}

	modBytes, err := client.GetModuleBazel(ctx, registryURL, k.name, k.version)
	if err != nil {
		return nil, err
	}

	sink, err := mw.BlobWriter(src.URL)
	if err != nil {
		return nil, err
	}
	body, vr, err := client.FetchArchive(ctx, src)
	if err != nil {
		sink.Abort()
		return nil, err
	}
	defer body.Close()
	if _, err := io.Copy(sink, body); err != nil {
		sink.Abort()
		return nil, fmt.Errorf("stream tarball: %w", err)
	}
	if err := vr.Verify(); err != nil {
		sink.Abort()
		return nil, fmt.Errorf("integrity: %w", err)
	}
	if _, _, _, err := sink.Close(); err != nil {
		return nil, fmt.Errorf("finalize blob: %w", err)
	}
	if err := mw.WriteSource(k.name, k.version, srcBytes); err != nil {
		return nil, err
	}
	if err := mw.WriteModuleBazel(k.name, k.version, modBytes); err != nil {
		return nil, err
	}
	// Lift upstream metadata.json registry-level fields (homepage,
	// maintainers, repository, yanked_versions) into the local copy
	// while we're already fetching from this registry. Best-effort:
	// a missing or unfetchable metadata.json doesn't fail the walk,
	// since the version merge underneath works without it.
	upstreamMeta, _ := client.GetMetadataBytes(ctx, registryURL, k.name)
	if err := mw.MergeMetadataWithUpstream(k.name, k.version, upstreamMeta); err != nil {
		return nil, err
	}

	return parseDeps(modBytes)
}

// parseDeps extracts bazel_dep(name=..., version=...) tuples from raw
// MODULE.bazel bytes. Skips dev_dependency entries and compatibility-
// only declarations with empty version.
func parseDeps(modBytes []byte) ([]moduleKey, error) {
	info, err := gobzlmod.ParseModuleContent(string(modBytes))
	if err != nil {
		return nil, fmt.Errorf("parse MODULE.bazel: %w", err)
	}
	var deps []moduleKey
	for _, d := range info.Dependencies {
		if d.DevDependency {
			continue
		}
		if strings.TrimSpace(d.Name) == "" || strings.TrimSpace(d.Version) == "" {
			continue
		}
		deps = append(deps, moduleKey{name: d.Name, version: d.Version})
	}
	return deps, nil
}

func emit(opts RecursiveOptions, ev RecursiveEvent) {
	if opts.Reporter != nil {
		opts.Reporter(ev)
	}
}
