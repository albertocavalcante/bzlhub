// Package scipio reads and writes SCIP index files.
//
// It is the only package that touches the wire encoding; merge and report
// operate on decoded messages so neither has to know how an index is framed.
//
// The writer emits an index incrementally -- metadata, then one document at a
// time, then the external symbols -- rather than marshaling a whole
// *scip.Index. A protobuf message is just a concatenation of length-delimited
// fields, so writing the pieces in field order produces exactly the same bytes
// as marshaling the whole, while never holding a second full copy in memory.
// Nothing upstream offers this.
package scipio

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// Field numbers from scip.proto's Index message.
const (
	fieldMetadata        = 1
	fieldDocuments       = 2
	fieldExternalSymbols = 3
)

// marshalOpts is used for every message scipd writes. Deterministic is a
// no-op for SCIP today -- the schema has no map fields -- but it states the
// intent, and it is the setting that keeps output reproducible if one is ever
// added.
var marshalOpts = proto.MarshalOptions{Deterministic: true}

// Writer emits a SCIP index incrementally.
//
// Call order must follow field order: Metadata, then Document any number of
// times, then ExternalSymbol any number of times. Writing out of order
// produces a technically-parseable but non-canonical index, so the writer
// enforces it rather than trusting the caller.
type Writer struct {
	w     io.Writer
	buf   []byte
	stage stage
	n     int64
}

type stage int

const (
	stageStart stage = iota
	stageMetadata
	stageDocuments
	stageExternals
)

// NewWriter returns a Writer that appends to w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// BytesWritten reports how many bytes have been emitted so far.
func (w *Writer) BytesWritten() int64 { return w.n }

// Metadata writes the index metadata. It must be called exactly once, before
// any document.
func (w *Writer) Metadata(md *scip.Metadata) error {
	if w.stage != stageStart {
		return fmt.Errorf("scipio: Metadata called after %v", w.stage)
	}
	w.stage = stageMetadata
	if md == nil {
		return nil
	}
	return w.field(fieldMetadata, md)
}

// Document writes one document.
func (w *Writer) Document(doc *scip.Document) error {
	if w.stage > stageDocuments {
		return fmt.Errorf("scipio: Document called after external symbols")
	}
	w.stage = stageDocuments
	if doc == nil {
		return nil
	}
	return w.field(fieldDocuments, doc)
}

// ExternalSymbol writes one external symbol.
func (w *Writer) ExternalSymbol(si *scip.SymbolInformation) error {
	w.stage = stageExternals
	if si == nil {
		return nil
	}
	return w.field(fieldExternalSymbols, si)
}

// field appends one length-delimited submessage.
//
// The scratch buffer is reused across calls: at LLVM scale this runs tens of
// millions of times, and a fresh allocation per occurrence is the difference
// between a merge that completes and one that does not.
func (w *Writer) field(num protowire.Number, m proto.Message) error {
	body, err := marshalOpts.Marshal(m)
	if err != nil {
		return fmt.Errorf("scipio: marshal field %d: %w", num, err)
	}
	w.buf = w.buf[:0]
	w.buf = protowire.AppendTag(w.buf, num, protowire.BytesType)
	w.buf = protowire.AppendBytes(w.buf, body)

	n, err := w.w.Write(w.buf)
	w.n += int64(n)
	if err != nil {
		return fmt.Errorf("scipio: write field %d: %w", num, err)
	}
	if n != len(w.buf) {
		return fmt.Errorf("scipio: write field %d: %w", num, io.ErrShortWrite)
	}
	return nil
}

func (s stage) String() string {
	switch s {
	case stageMetadata:
		return "metadata"
	case stageDocuments:
		return "documents"
	case stageExternals:
		return "external symbols"
	default:
		return "start"
	}
}

// WriteIndex emits a whole index through Writer. It exists so callers with an
// in-memory index get the identical byte stream as an incremental writer,
// which is what makes the streaming path testable against the simple one.
func WriteIndex(w io.Writer, idx *scip.Index) (int64, error) {
	if idx == nil {
		return 0, nil
	}
	iw := NewWriter(w)
	if err := iw.Metadata(idx.Metadata); err != nil {
		return iw.BytesWritten(), err
	}
	for _, doc := range idx.Documents {
		if err := iw.Document(doc); err != nil {
			return iw.BytesWritten(), err
		}
	}
	for _, si := range idx.ExternalSymbols {
		if err := iw.ExternalSymbol(si); err != nil {
			return iw.BytesWritten(), err
		}
	}
	return iw.BytesWritten(), nil
}

// WriteIndexFile writes idx to a temporary file beside path, then replaces path.
//
// Note the absence of `defer f.Close()`. On a *write*, Close is where a failed
// flush is reported -- a full disk most of all -- so deferring it discards the
// one error that says the file on disk is short. The success path returns
// Close's error; the failure path closes only to release the descriptor,
// having already got a better error to return.
//
// Callers that support "-" for stdout should handle that themselves and call
// WriteIndex: which writer stdin/stdout maps to is CLI policy, not file I/O.
func WriteIndexFile(path string, idx *scip.Index) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("scipio: create replacement for %s: %w", path, err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if old, err := os.Stat(path); err == nil {
		if err := f.Chmod(old.Mode().Perm()); err != nil {
			_ = f.Close()
			return fmt.Errorf("scipio: preserve mode for %s: %w", path, err)
		}
	}
	if _, err := WriteIndex(f, idx); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("scipio: sync replacement for %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("scipio: close %s: %w", path, err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("scipio: replace %s: %w", path, err)
	}
	return nil
}

// ReadIndexFile reads an index from path.
func ReadIndexFile(path string) (*scip.Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("scipio: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return ReadIndex(f)
}

// ReadIndex decodes an index from r.
//
// v0.1 reads the whole index into memory; the streaming reader upstream
// provides (scip.IndexVisitor.ParseStreaming) is the drop-in for when the
// merge itself becomes streaming.
func ReadIndex(r io.Reader) (*scip.Index, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("scipio: read: %w", err)
	}
	return UnmarshalIndex(b)
}

// UnmarshalIndex decodes an index from bytes.
func UnmarshalIndex(b []byte) (*scip.Index, error) {
	var idx scip.Index
	if err := proto.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("scipio: unmarshal: %w", err)
	}
	return &idx, nil
}
