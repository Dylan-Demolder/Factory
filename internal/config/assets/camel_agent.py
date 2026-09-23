#!/usr/bin/env python3
"""Bridge between factory and a CAMEL-AI ChatAgent.

factory writes the full prompt to stdin; this script prints the agent's reply
to stdout. Configure it with environment variables (set them in the agent's
"env" block in factory.json):

  CAMEL_PLATFORM     CAMEL ModelPlatformType value, e.g. "openai",
                     "anthropic", "openai-compatible-model", "ollama"
  CAMEL_MODEL        model name for that platform
  CAMEL_BASE_URL     endpoint URL (for openai-compatible / self-hosted models)
  CAMEL_API_KEY_ENV  name of the env var that holds the API key
  CAMEL_SYSTEM       optional system message

factory also sets, per call:

  FACTORY_READONLY   "true" for reviewers, interviewers and roundtable seats;
                     "false" for the builder. This decides which tools the
                     agent is offered — a reviewer gets read tools only, a
                     builder additionally gets the ability to create and
                     edit files inside the project directory, and to set the
                     executable bit (write_file always makes 0644, so a task
                     requiring an executable script was otherwise impossible
                     to satisfy).

Without tools this bridge could only ever *talk* about building; factory
expects a builder to leave changed files behind, so the tool set is what
makes a camel agent usable as a builder rather than as a commenter.

Install: pip install camel-ai "mcp<2"
If your camel agents already run behind an HTTP service, you can instead
replace this script with a "curl" call or an "openai"-type agent.
"""
import fnmatch
import logging
import os
import re
import sys

# Bound the work a single tool call can do, so a wild pattern cannot turn a
# review into a full-disk scan.
MAX_READ_BYTES = 256 * 1024
MAX_LIST_FILES = 2000
MAX_GREP_HITS = 200
SKIP_DIRS = {".git", ".factory", "node_modules", "__pycache__", ".venv", "venv"}


class _DropContextWindowNoise(logging.Filter):
    """Silence camel's warning that it cannot size an unknown model.

    `auto` is camelStream's documented model ID, and camel's client has no
    entry for it, so every call logs "context window size not defined.
    Defaulting to 999_999_999." on the root logger. That leaks into factory's
    captured output and makes a healthy reply look like an error.

    The 999_999_999 default is deliberately left alone: camel uses
    `token_limit` to decide whether to trim messages *before sending them*,
    while camelStream compacts the middle of the conversation itself and
    keeps message envelopes in place. Clamping it to the documented 260K here
    would discard messages locally that the service would have compacted
    intact. Only this one message is filtered — genuine warnings still show.
    """

    NEEDLE = "context window size not defined"

    def filter(self, record: logging.LogRecord) -> bool:
        try:
            return self.NEEDLE not in record.getMessage()
        except Exception:
            return True


# The warning comes from `logging.warning(...)`, i.e. the root logger, so a
# filter on it is where it has to be caught.
logging.getLogger().addFilter(_DropContextWindowNoise())


# ---- tools ----
#
# Every path is resolved against the project directory (factory runs this
# script with cwd set there) and rejected if it escapes. Tools return a
# message rather than raising, so a bad call becomes feedback the model can
# correct instead of a crashed build.

def _resolve(path: str) -> str:
    """Resolve path inside the project directory, or raise ValueError."""
    base = os.path.realpath(os.getcwd())
    if not path or os.path.isabs(path):
        raise ValueError("path must be relative to the project directory")
    resolved = os.path.realpath(os.path.join(base, path))
    if resolved != base and not resolved.startswith(base + os.sep):
        raise ValueError("path escapes the project directory")
    return resolved


def _walk(pattern: str):
    """Yield project files matching a glob, skipping noise directories."""
    base = os.path.realpath(os.getcwd())
    kept = 0
    for root, dirs, files in os.walk(base):
        dirs[:] = [d for d in dirs if d not in SKIP_DIRS]
        for name in files:
            rel = os.path.relpath(os.path.join(root, name), base)
            if fnmatch.fnmatch(rel, pattern) or fnmatch.fnmatch(name, pattern):
                yield rel
                kept += 1
                if kept >= MAX_LIST_FILES:
                    return


def read_file(path: str) -> str:
    """Read a text file from the project. Returns its contents, truncated
    if larger than 256KB."""
    try:
        target = _resolve(path)
        with open(target, "r", encoding="utf-8", errors="replace") as fh:
            data = fh.read(MAX_READ_BYTES + 1)
    except Exception as exc:  # noqa: BLE001 - the model needs the reason
        return f"error: {exc}"
    if len(data) > MAX_READ_BYTES:
        return data[:MAX_READ_BYTES] + f"\n…[truncated at {MAX_READ_BYTES} bytes]"
    return data


def list_files(pattern: str = "*") -> str:
    """List project files matching a glob pattern (default: every file)."""
    try:
        found = sorted(_walk(pattern or "*"))
    except Exception as exc:  # noqa: BLE001
        return f"error: {exc}"
    if not found:
        return f"no files match {pattern!r}"
    body = "\n".join(found[:MAX_LIST_FILES])
    if len(found) >= MAX_LIST_FILES:
        body += f"\n…[stopped at {MAX_LIST_FILES} files]"
    return body


def grep_files(pattern: str, glob_pattern: str = "*") -> str:
    """Search project files for a regular expression; returns file:line hits."""
    try:
        rx = re.compile(pattern)
    except re.error as exc:
        return f"error: bad regular expression: {exc}"
    hits, scanned = [], 0
    for rel in _walk(glob_pattern or "*"):
        try:
            with open(_resolve(rel), "r", encoding="utf-8", errors="replace") as fh:
                for lineno, line in enumerate(fh, 1):
                    if rx.search(line):
                        hits.append(f"{rel}:{lineno}: {line.rstrip()[:200]}")
                        if len(hits) >= MAX_GREP_HITS:
                            break
        except Exception:
            continue  # binary or unreadable: skip, do not fail the search
        scanned += 1
        if len(hits) >= MAX_GREP_HITS:
            break
    if not hits:
        return f"no matches for {pattern!r} in {scanned} file(s)"
    return "\n".join(hits) + ("" if len(hits) < MAX_GREP_HITS else f"\n…[{MAX_GREP_HITS} hits]")


def write_file(path: str, content: str) -> str:
    """Create or overwrite a file in the project with the given contents."""
    try:
        target = _resolve(path)
        os.makedirs(os.path.dirname(target) or ".", exist_ok=True)
        with open(target, "w", encoding="utf-8") as fh:
            fh.write(content)
    except Exception as exc:  # noqa: BLE001
        return f"error: {exc}"
    return f"wrote {len(content)} characters to {path}"


def replace_in_file(path: str, old: str, new: str) -> str:
    """Replace an exact, unique occurrence of `old` with `new` in a file."""
    if not old:
        return "error: nothing to replace"
    try:
        target = _resolve(path)
        with open(target, "r", encoding="utf-8", errors="replace") as fh:
            data = fh.read()
    except Exception as exc:  # noqa: BLE001
        return f"error: {exc}"
    count = data.count(old)
    if count == 0:
        return "error: the exact text to replace was not found; read the file first"
    if count > 1:
        return f"error: that text occurs {count} times; include more surrounding context to make it unique"
    with open(target, "w", encoding="utf-8") as fh:
        fh.write(data.replace(old, new, 1))
    return f"replaced 1 occurrence in {path}"


def make_executable(path: str) -> str:
    """Set the executable bit on a project file (chmod +x).

    Without this a builder can never satisfy a criterion that asks for an
    executable script: write_file always creates 0644 and there was no way to
    change the mode — wordcnt's reviewer rejected a task four times over
    exactly that.
    """
    try:
        target = _resolve(path)
        if not os.path.exists(target):
            return f"error: {path} does not exist yet — create it first"
        os.chmod(target, os.stat(target).st_mode | 0o111)
    except Exception as exc:  # noqa: BLE001
        return f"error: {exc}"
    return f"{path} is now executable"


# Reviewers, interviewers and roundtable seats must not touch files — they
# are only ever offered read tools, so editing is not something the model can
# even attempt.
READ_TOOLS = (read_file, list_files, grep_files)
WRITE_TOOLS = (write_file, replace_in_file, make_executable)

BUILDER_RULES = (
    "\n\nYou are the builder for this project. Work inside the project "
    "directory only. Use the file tools to create and edit real files, keep "
    "changes focused on the task, and finish by summarising what you changed "
    "and which tests cover it. Never claim a file was written unless a tool "
    "call succeeded."
)

READER_RULES = (
    "\n\nYou are a reviewer/advisor. You have read-only tools: inspect the "
    "project, but do not attempt to change anything, and do not claim to "
    "have changed anything."
)


def main() -> int:
    prompt = sys.stdin.read()
    if not prompt.strip():
        print("empty prompt", file=sys.stderr)
        return 2

    try:
        from camel.agents import ChatAgent
        from camel.models import ModelFactory
        from camel.toolkits import FunctionTool
        from camel.types import ModelPlatformType
    except ModuleNotFoundError as e:
        # A package is genuinely absent. ModuleNotFoundError is a subclass of
        # ImportError, so it has to be caught first.
        print(
            f"camel-ai is not installed ({e}). Run: pip install camel-ai",
            file=sys.stderr,
        )
        return 3
    except ImportError as e:
        # camel-ai is present but one of its own imports failed. As of
        # camel-ai 0.2.x the usual cause is an unpinned transitive dependency:
        # camel-ai requires mcp>=1.3.0 with no upper bound, and mcp 2.x
        # removed FastMCP, so an unpinned install cannot import.
        print(
            f"camel-ai is installed but failed to import: {e}. "
            "Try: pip install 'mcp<2'",
            file=sys.stderr,
        )
        return 3

    platform = os.environ.get("CAMEL_PLATFORM", "openai-compatible-model")
    model_name = os.environ.get("CAMEL_MODEL")
    if not model_name or model_name == "REPLACE_ME":
        print("set CAMEL_MODEL in the agent's env block", file=sys.stderr)
        return 2
    base_url = os.environ.get("CAMEL_BASE_URL") or None
    if base_url == "REPLACE_ME":
        base_url = None
    key_env = os.environ.get("CAMEL_API_KEY_ENV")
    api_key = os.environ.get(key_env) if key_env else None

    readonly = os.environ.get("FACTORY_READONLY", "false").lower() == "true"
    tools = READ_TOOLS if readonly else READ_TOOLS + WRITE_TOOLS

    system = os.environ.get(
        "CAMEL_SYSTEM",
        "You are a careful, senior member of a software product team. "
        "Follow the instructions in the user's message exactly, including any "
        "required output format.",
    ) + (READER_RULES if readonly else BUILDER_RULES)

    # Anything raised here (missing credentials, a bad endpoint, a transport
    # error) is reported as one line: a traceback tells factory nothing that
    # "agent camel-a: <error>" doesn't say better.
    try:
        model = ModelFactory.create(
            model_platform=ModelPlatformType(platform),
            model_type=model_name,
            api_key=api_key,
            url=base_url,
        )
        agent = ChatAgent(
            system_message=system,
            model=model,
            tools=[FunctionTool(fn) for fn in tools],
        )
        response = agent.step(prompt)
    except Exception as e:  # noqa: BLE001
        print(f"{type(e).__name__}: {e}", file=sys.stderr)
        return 1
    if not response.msgs:
        print("agent returned no message", file=sys.stderr)
        return 1
    print(response.msgs[0].content)
    return 0


if __name__ == "__main__":
    sys.exit(main())
