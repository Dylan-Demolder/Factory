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

Install: pip install camel-ai
If your CAMEL agents already run behind an HTTP service, you can instead
replace this script with a `curl` call or an "openai"-type agent.
"""
import os
import sys


def main() -> int:
    prompt = sys.stdin.read()
    if not prompt.strip():
        print("empty prompt", file=sys.stderr)
        return 2

    try:
        from camel.agents import ChatAgent
        from camel.models import ModelFactory
        from camel.types import ModelPlatformType
    except ImportError:
        print("camel-ai is not installed: pip install camel-ai", file=sys.stderr)
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

    model = ModelFactory.create(
        model_platform=ModelPlatformType(platform),
        model_type=model_name,
        api_key=api_key,
        url=base_url,
    )
    system = os.environ.get(
        "CAMEL_SYSTEM",
        "You are a careful, senior member of a software product team. "
        "Follow the instructions in the user's message exactly, including any "
        "required output format.",
    )
    agent = ChatAgent(system_message=system, model=model)
    response = agent.step(prompt)
    if not response.msgs:
        print("agent returned no message", file=sys.stderr)
        return 1
    print(response.msgs[0].content)
    return 0


if __name__ == "__main__":
    sys.exit(main())
