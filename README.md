# newsops

A personal news pipeline: the agent judges, researches, and drafts; the CLI collects and remembers; the human decides. Collect a daily digest of ~40 candidates, skim and rank them, then research individual stories into briefs and optionally draft posts for LinkedIn or a blog.

## Install

```
make install          # builds and copies to ~/.local/bin/newsops
export PATH=$HOME/.local/bin:$PATH   # or wherever PREFIX points
newsops version       # should report a real version and commit
```

## Daily loop (three commands)

```
newsops collect       →  ~40 candidates, written to digest.md
brief <id>            →  research brief at briefs/<id>-<slug>.md
draft <id> --linkedin →  draft at posts/<date>-<slug>-linkedin.md
```

## More

The skill router at `.agents/skills/news-ops/SKILL.md` is the entrypoint for agentic invocation. Read `AGENTS.md` for the full CLI reference, mode definitions, and workflow details.

Run `make skills` after cloning to create harness symlinks: every directory under `.agents/skills/` is linked as a directory into `.claude/skills/`, `.cursor/skills/` and `.opencode/skills/`. If your checkout landed symlinks as plain files or directories (git `core.symlinks` is false), `make skills` repairs them.
