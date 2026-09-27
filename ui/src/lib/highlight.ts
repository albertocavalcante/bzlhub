import { createHighlighterCore } from 'shiki/core';
import { createJavaScriptRegexEngine } from 'shiki/engine/javascript';
import bash from 'shiki/langs/bash.mjs';
import go from 'shiki/langs/go.mjs';
import javascript from 'shiki/langs/javascript.mjs';
import json from 'shiki/langs/json.mjs';
import markdown from 'shiki/langs/markdown.mjs';
import python from 'shiki/langs/python.mjs';
import toml from 'shiki/langs/toml.mjs';
import typescript from 'shiki/langs/typescript.mjs';
import yaml from 'shiki/langs/yaml.mjs';
import githubLight from 'shiki/themes/github-light.mjs';

const highlighter = createHighlighterCore({
  themes: [githubLight],
  langs: [bash, go, javascript, json, markdown, python, toml, typescript, yaml],
  engine: createJavaScriptRegexEngine(),
});

export async function codeToHtml(
  code: string,
  options: { lang: string; theme: string },
): Promise<string> {
  const instance = await highlighter;
  return instance.codeToHtml(code, options);
}
