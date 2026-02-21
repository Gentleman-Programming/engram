# Engram Multi-Agent Memory Guide

This guide covers using Engram in Claude Code projects that orchestrate multiple specialized
AI agents via the Task() pattern.

## What is a Multi-Agent Workflow?

In Claude Code, you can launch specialized subagents to handle specific tasks:

```python
# Claude Code orchestration example
Task(subagent_type="backend", prompt="Implement the user authentication endpoint")
Task(subagent_type="testing", prompt="Write tests for the auth endpoint")
Task(subagent_type="security-reviewer", prompt="Audit the auth implementation for OWASP issues")
```

Each subagent is an independent AI instance with its own transcript. Without Engram, when
each subagent finishes, its knowledge is gone. With Engram, every learning is persisted.

## Memory Flow in Multi-Agent Workflows

```
Orchestrator (parent)
    |
    +-- Task(subagent_type="backend")
    |       |
    |       +-- Works: implements auth endpoint
    |       +-- Calls mem_save("Chose JWT over sessions", type="decision")
    |       +-- Outputs: "## Key Learnings:\n1. bcrypt cost=12 is..."
    |               |
    |               +-- SubagentStop fires --------------------------+
    |                                                                |
    +-- Task(subagent_type="testing")                                |
    |       |                                                        v
    |       +-- Calls mem_search("auth JWT") <-- finds backend's     |
    |       |       saved memories                                   |
    |       +-- Outputs: "## Key Learnings:\n1. Mock JWT..."         |
    |               |                                                |
    |               +-- SubagentStop fires ----------------------+   |
    |                                                            |   |
    +-- Session ends                                             |   |
            |                                                    v   v
            +-- Stop hook checks for mem_session_summary    Engram DB
                                                           (all learnings
                                                            preserved by
                                                            agent_name)
```

## How Passive Capture Works

The `subagent-stop.sh` hook fires after every Task() completion. It:

1. Reads the subagent's transcript
2. Looks for `## Key Learnings:`, `## Aprendizajes Clave:`, or `### Learnings:` sections
3. Extracts each numbered or bulleted item
4. Saves each as an observation with `agent_name` in metadata
5. Logs to `~/.cache/engram/logs/subagent-stop.log`

This runs asynchronously and never blocks the parent agent.

## Active vs Passive Capture

Both mechanisms work together as defense in depth:

| Mechanism | How | When to Rely On |
|-----------|-----|-----------------|
| **Active** (agent calls mem_save) | Agent decides what's worth saving | High-value decisions, specific bugfixes |
| **Passive** (SubagentStop hook) | Hook extracts from transcript | Safety net, end-of-task learnings |

**Best practice:** Agents should do both:
1. Call `mem_save` for important decisions during the task
2. End response with `## Key Learnings:` section for the hook to capture

## Teaching Your Agents to Emit Learnings

Add this to your agent prompts or SKILL.md:

```markdown
## Output Requirement

AT THE END of your response, include:

## Key Learnings:

1. [Specific technical insight from this task]
2. [Pattern or best practice applied]
3. [Reusable knowledge for future tasks]
```

The SubagentStop hook reads this section and saves each item to Engram automatically.

## Topic Key Namespacing for Agent Teams

When multiple agents work on the same project, use agent-prefixed topic_keys to prevent
collision:

```
backend/architecture/auth-model     <-- @backend agent's auth decisions
frontend/architecture/auth-model    <-- @frontend agent's auth decisions
security/architecture/auth-model    <-- @security-reviewer's auth findings
```

**Why this matters:** Without namespacing, if `@backend` saves with `topic_key="auth-model"`
and `@frontend` also saves with `topic_key="auth-model"`, one overwrites the other. With
prefixes, both coexist.

**Pattern:**
```
{agent_name}/{category}/{topic}
```

**Common categories:**
- `architecture/` — design decisions
- `patterns/` — discovered patterns
- `bugfixes/` — root causes
- `conventions/` — team agreements
- `config/` — environment and tooling

## Searching Memories Across Agents

To find what any agent learned about a topic:
```
mem_search(query="JWT authentication")
```

To find what a specific agent learned:
```
mem_search(query="backend JWT authentication")
# or use metadata filter when available
mem_search(query="JWT", filter={"agent_name": "backend"})
```

## Session Summary in Multi-Agent Workflows

The orchestrator (parent agent) should call `mem_session_summary` at the end, summarizing
the combined work of all subagents:

```
mem_session_summary(
  goal="Implement user authentication with JWT",
  discoveries=[
    "backend: bcrypt cost=12 is the right balance for our server",
    "testing: JWT mock requires specific HS256 algorithm in fixtures",
    "security: refresh token rotation must be atomic to prevent race"
  ],
  accomplished=[
    "Implemented /auth/login, /auth/refresh, /auth/logout",
    "Full test suite with 94% coverage",
    "Security audit passed -- 0 OWASP issues"
  ],
  next_steps=["Add rate limiting to /auth/login"],
  relevant_files=["api/auth.py", "tests/test_auth.py"]
)
```

## Debugging

**Check what was captured by SubagentStop:**
```bash
cat ~/.cache/engram/logs/subagent-stop.log | tail -50
```

**Check what session-stop found:**
```bash
cat ~/.cache/engram/logs/session-stop.log | tail -20
```

**Search for learnings from a specific agent:**
```bash
engram search "backend"
# or in TUI
engram tui
```

**Verify hook is registered:**
```bash
cat plugin/claude-code/hooks/hooks.json | jq '.hooks.SubagentStop'
```

## Anti-Patterns to Avoid

**Do NOT use generic topic_keys across agents:**
```
# Wrong -- frontend will overwrite backend's decisions
topic_key="auth-model"

# Correct -- scoped to agent
topic_key="backend/architecture/auth-model"
```

**Do NOT skip mem_session_summary in the orchestrator:**
```
# Wrong -- subagent learnings are saved but session context is lost
Task("backend") -> Task("testing") -> "Done!"

# Correct
Task("backend") -> Task("testing") -> mem_session_summary(...) -> "Done!"
```

**Do NOT search only your own memories:**
```
# Wrong -- misses learnings from other agents in the team
mem_search("auth backend/auth-model")

# Correct -- broad search across all agents
mem_search("authentication JWT")
```
