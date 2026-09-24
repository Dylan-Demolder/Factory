# Security Policy

## Reporting a vulnerability

Please **do not** open a public issue for a security vulnerability.

Report it privately through
[GitHub Security Advisories](https://github.com/Dylan-Demolder/Factory/security/advisories/new),
or contact the maintainer via their GitHub profile if that form is unavailable.

Include whatever you have:

- what the issue is
- how to reproduce it
- what you expected versus what happened
- the impact, if you know it

A rough report that turns out to be real is far more useful than a polished
one you never sent.

You will get an acknowledgement when the report is read. Please give a
reasonable amount of time to investigate and fix before disclosing publicly,
and keep the details private until a fix is available.

## What is in scope

- the `factory` binary and its bundled web interface
- the HTTP API and its authentication, session, CSRF and CORS handling
- path handling around project names and the artifact viewer
- the agent bridge (`internal/config/assets/camel_agent.py`) and the sandbox
  it applies to file and command access

## Out of scope

- vulnerabilities in the coding agents factory shells out to — `opencode`,
  `claude`, CAMEL, or any CLI you configure. factory does not vendor them,
  and their security posture is theirs.
- prompt injection that causes an agent to write unwanted *code*. Agents are
  given file and command access by design; see the
  [security model](README.md#security-model) for the boundaries factory does
  enforce, and run it as a dedicated user or in a container if you need
  stronger isolation than that.
- social engineering, and issues requiring physical access to the machine
  running factory

## Supported versions

Only the latest release receives fixes. There are no long-term support
branches.
