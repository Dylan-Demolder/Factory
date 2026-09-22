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

Install: pip install camel-ai "mcp<2"
If your CAMEL agents already run behind an HTTP service, you can instead
replace this script with a `curl` call or an "openai"-type agent.
"""
import logging
import os
import sys


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


def main() -> int:
    prompt = sys.stdin.read()
    if not prompt.strip():
        print("empty prompt", file=sys.stderr)
        return 2

    try:
        from camel.agents import ChatAgent
        from camel.models import ModelFactory
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
        # removed FastMCP, so pip resolves a combination that cannot import.
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

    system = os.environ.get(
        "CAMEL_SYSTEM",
        "You are a careful, senior member of a software product team. "
        "Follow the instructions in the user's message exactly, including any "
        "required output format.",
    )
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
        agent = ChatAgent(system_message=system, model=model)
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
