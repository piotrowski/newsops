VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
PREFIX  ?= $(HOME)/.local
HARNESSES ?= .claude .opencode .cursor

.PHONY: build install skills test e2e check sources clean

build:
	go build -ldflags "-X main.Version=$(VERSION) -X main.Commit=$(COMMIT)" -o newsops ./cmd/newsops

install: build
	mkdir -p $(PREFIX)/bin
	cp newsops $(PREFIX)/bin/

# Every directory under .agents/skills/ is one skill (a real SKILL.md). Each
# harness gets a directory symlink per skill, never a copy. Idempotent, and
# repairs a checkout where the links landed as plain files or directories.
skills:
	@for harness in $(HARNESSES); do \
		mkdir -p "$$harness/skills"; \
		for dir in .agents/skills/*/; do \
			name=$$(basename "$$dir"); \
			target="$$harness/skills/$$name"; \
			rm -rf "$$target"; \
			ln -s "../../.agents/skills/$$name" "$$target"; \
		done; \
	done

test:
	go test ./...

# End-to-end: drives the built binary through every subcommand against local
# fixture feeds (no network). See e2e/e2e_test.go.
e2e: build
	NEWSOPS_BIN=$(CURDIR)/newsops go test -tags e2e -count=1 ./e2e/

check: build test e2e skills
	@for f in $$(grep -o 'modes/[a-z_]*\.md' .agents/skills/news-ops/SKILL.md); do \
		test -f "$$f" || { echo "missing: $$f"; exit 1; }; \
	done
	@for dir in .agents/skills/*/; do \
		name=$$(basename "$$dir"); \
		test -f "$$dir/SKILL.md" || { echo "missing: $$dir/SKILL.md"; exit 1; }; \
		for harness in $(HARNESSES); do \
			target=$$harness/skills/$$name; \
			[ -L "$$target" ] || { echo "not a symlink: $$target"; exit 1; }; \
			[ -f "$$target/SKILL.md" ] || { echo "broken symlink: $$target -> $$(readlink "$$target")"; exit 1; }; \
		done; \
	done

# Network reachability of the feed list. Separate from check: feeds rot
# independently of the code, and a dead feed is not a build failure.
sources: build
	./newsops sources:check --json

clean:
	rm -f newsops
