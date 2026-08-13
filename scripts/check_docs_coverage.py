"""Fail when something in the code appears in no document.

Three surfaces are checked, because each one is a thing a reader looks up and
expects to find:

  * every yaml-tagged leaf field of internal/config
  * every builtin tool in internal/tools
  * every HTTP route registered anywhere

A key that is documented nowhere is worse than a key with a thin description.
The reader cannot tell whether it is absent because it does not exist, or
because nobody wrote it down, and that doubt spreads to the whole document.

Run it from the repository root:

    python scripts/check_docs_coverage.py

The Pages workflow runs the same file, so a local pass means a CI pass.
"""

import glob
import os
import re
import sys

DOC_GLOBS = ["docs/site/*.html", "docs/*.md", "index.html"]


def read_docs():
    """Join every document into one string. Presence is what matters here."""
    out = []
    for pattern in DOC_GLOBS:
        for path in glob.glob(pattern):
            with open(path, encoding="utf-8") as fh:
                out.append(fh.read())
    return "\n".join(out)


def config_leaves():
    """Give every leaf config path, as a reader would write it in YAML.

    The walk follows a field into its own struct when the type names one, and
    records a path only at a leaf. A section head is not a setting, so it is
    not something to document on its own.
    """
    with open("internal/config/config.go", encoding="utf-8") as fh:
        src = fh.read()

    structs = {}
    for match in re.finditer(r"type (\w+) struct \{(.*?)\n\}", src, re.S):
        fields = re.findall(
            r"^\t(\w+)\s+([\w\.\*\[\]]+)\s+`[^`]*yaml:\"([^\",]+)",
            match.group(2),
            re.M,
        )
        structs[match.group(1)] = fields

    leaves = []
    seen = set()

    def walk(name, prefix):
        if name not in structs or (name, prefix) in seen:
            return
        seen.add((name, prefix))
        for _, gotype, key in structs[name]:
            base = gotype.lstrip("*[]")
            path = prefix + key
            if base in structs:
                walk(base, path + ".")
            else:
                leaves.append(path)

    walk("Config", "")
    return leaves


def builtin_tools():
    names = set()
    for path in glob.glob("internal/tools/builtin_*.go"):
        with open(path, encoding="utf-8") as fh:
            names |= set(re.findall(r'Name\(\) string\s*\{ return "([a-z_]+)" \}', fh.read()))
    return sorted(names)


def http_routes():
    names = set()
    sources = glob.glob("internal/**/*.go", recursive=True)
    sources += glob.glob("cmd/**/*.go", recursive=True)
    for path in sources:
        if path.endswith("_test.go"):
            continue
        with open(path, encoding="utf-8") as fh:
            names |= set(re.findall(r'(?:HandleFunc|mux\.Handle)\("([^"]+)"', fh.read()))
    return sorted(names)


def main():
    if not os.path.exists("internal/config/config.go"):
        print("run this from the repository root")
        return 2

    docs = read_docs()
    missing = []

    leaves = config_leaves()
    for key in leaves:
        # A leaf name on its own counts. Pages document `token_budget` under a
        # `context.background` heading, and that is a complete answer.
        if key not in docs and key.split(".")[-1] not in docs:
            missing.append("config key  " + key)

    tools = builtin_tools()
    for tool in tools:
        if tool not in docs:
            missing.append("tool        " + tool)

    routes = http_routes()
    for route in routes:
        if route.rstrip("/") not in docs and route not in docs:
            missing.append("route       " + route)

    if missing:
        print("These exist in the code and appear in no document:")
        print()
        for item in missing:
            print("  " + item)
        print()
        print("Document them, or the reader cannot tell whether a thing is absent")
        print("because it does not exist or because nobody wrote it down.")
        return 1

    print(
        "documented: %d config keys, %d tools, %d routes"
        % (len(leaves), len(tools), len(routes))
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
