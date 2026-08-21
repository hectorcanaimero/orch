# openspec/

Spec-Driven Development artifacts live here.

## Layout

```
openspec/
├── changes/          # in-flight change proposals (one dir per change)
│   └── <name>/
│       ├── proposal.md
│       ├── spec.md
│       ├── design.md
│       └── tasks.md
└── specs/            # merged / archived specs (source of truth)
```

## Workflow

If you have the SDD skills installed under `~/.claude/skills/`, drive the flow
from Claude Code:

1. `/sdd-explore <topic>` — investigate the requirement
2. `/sdd-new <change-name>` — proposal + explore
3. `/sdd-ff <change-name>` — fast-forward proposal → spec → design → tasks
4. `/sdd-apply <change-name>` — implement
5. `/sdd-verify <change-name>` — validate
6. `/sdd-archive <change-name>` — merge into `openspec/specs/` + close

Once `changes/<name>/tasks.md` exists, hand it to orch:

```bash
# Preview diff first (dry-run)
orch atomize --file openspec/changes/<name>/tasks.md

# Apply
orch atomize --file openspec/changes/<name>/tasks.md --apply

# Dispatch
orch --mode auto
```

## SDD not installed?

Install the skills from your preferred source (Claude Code plugin, Copilot CLI
plugin, or manual clone into `~/.claude/skills/`). Or skip SDD entirely and
write specs by hand under `specs/`.
