"""
AgentRewind Demo Agent — 5-step PR review pipeline with OTel observability.

Supports mock mode (default) and real OpenAI mode via USE_REAL_LLM env var.
On startup, checks for recovery annotation from the AgentRewind controller
and skips already-completed steps.
"""

import json
import os
import sys
import time
import uuid
from typing import Any, TypedDict

import psycopg2
from langgraph.graph import StateGraph, END
from opentelemetry import trace
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.sdk.resources import Resource
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

USE_REAL_LLM = os.environ.get("USE_REAL_LLM", "false").lower() == "true"
OTEL_ENDPOINT = os.environ.get("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector:4318")
POSTGRES_DSN = os.environ.get("POSTGRES_DSN", "")
POD_NAME = os.environ.get("POD_NAME", "unknown")
POD_NAMESPACE = os.environ.get("POD_NAMESPACE", "default")
ANNOTATIONS_PATH = "/etc/podinfo/annotations"

# Pricing: gpt-4o-mini
PRICE_INPUT_PER_M = 0.15   # $0.15 per 1M input tokens
PRICE_OUTPUT_PER_M = 0.60  # $0.60 per 1M output tokens

RUN_ID = str(uuid.uuid4())

# Colors for terminal output
GREEN = "\033[92m"
YELLOW = "\033[93m"
CYAN = "\033[96m"
RED = "\033[91m"
BOLD = "\033[1m"
RESET = "\033[0m"

# ---------------------------------------------------------------------------
# Step definitions
# ---------------------------------------------------------------------------

STEPS = [
    {
        "index": 0,
        "name": "fetch_pr",
        "prompt": "Fetch the metadata for PR #42 in the acme/widget repo. Return the title, author, base branch, and file list.",
        "mock_response": "PR #42: 'Add caching layer for API responses'\nAuthor: @jane-dev\nBase: main\nFiles changed: src/cache.py, src/api/handler.py, tests/test_cache.py\n3 files changed, +187 -23 lines",
        "mock_tokens_in": 820,
        "mock_tokens_out": 210,
    },
    {
        "index": 1,
        "name": "analyze_diff",
        "prompt": "Analyze the following code diff for correctness, logic errors, and potential bugs:\n\n```python\nclass CacheLayer:\n    def __init__(self, ttl=300):\n        self._store = {}\n        self.ttl = ttl\n    def get(self, key):\n        entry = self._store.get(key)\n        if entry and time.time() - entry['ts'] < self.ttl:\n            return entry['val']\n        return None\n    def set(self, key, val):\n        self._store[key] = {'val': val, 'ts': time.time()}\n```\n\nIdentify any issues.",
        "mock_response": "Analysis complete:\n1. Thread safety: _store dict is not thread-safe for concurrent access. Consider using threading.Lock.\n2. Memory: No eviction policy — cache grows unbounded. Add max_size with LRU eviction.\n3. The get() method doesn't clean up expired entries, leading to stale data accumulation.\n4. No error handling for non-hashable keys.\nSeverity: Medium. Recommend fixes before merge.",
        "mock_tokens_in": 1500,
        "mock_tokens_out": 780,
    },
    {
        "index": 2,
        "name": "check_style",
        "prompt": "Review this Python code for style compliance with PEP 8 and project conventions:\n\n```python\nclass CacheLayer:\n    def __init__(self, ttl=300):\n        self._store = {}\n        self.ttl = ttl\n```\n\nCheck naming, formatting, docstrings, and type hints.",
        "mock_response": "Style review:\n1. Missing module-level docstring\n2. Missing class docstring explaining CacheLayer purpose\n3. No type hints on __init__ parameters or return types\n4. Consider using dataclass or NamedTuple for cache entries instead of plain dict\n5. Private attribute _store follows convention correctly\nOverall: Minor style issues. Add docstrings and type hints.",
        "mock_tokens_in": 1180,
        "mock_tokens_out": 620,
    },
    {
        "index": 3,
        "name": "security_scan",
        "prompt": "Scan this code for security vulnerabilities, injection risks, and unsafe patterns:\n\n```python\nclass CacheLayer:\n    def get(self, key):\n        entry = self._store.get(key)\n        if entry and time.time() - entry['ts'] < self.ttl:\n            return entry['val']\n        return None\n```\n\nCheck for: injection, timing attacks, information leakage, DoS vectors.",
        "mock_response": "Security scan results:\n1. DoS vector: Unbounded cache allows memory exhaustion attack. An attacker could fill the cache with arbitrary keys to cause OOM.\n2. No input validation on keys — if keys contain user input, consider sanitization.\n3. Timing side-channel: time.time() comparison could leak cache state via timing analysis (low risk for this use case).\n4. No encryption at rest — cached values stored in plaintext.\nRisk level: Low-Medium. Fix the unbounded growth issue.",
        "mock_tokens_in": 1820,
        "mock_tokens_out": 980,
    },
    {
        "index": 4,
        "name": "write_review",
        "prompt": "Based on the analysis, style check, and security scan results, write a concise PR review comment for PR #42 'Add caching layer'. Include a summary, list of issues by severity, and an overall recommendation (approve/request changes).",
        "mock_response": "## PR Review: Add caching layer for API responses\n\n**Recommendation: Request Changes**\n\n### Critical\n- Add thread safety (threading.Lock) for concurrent access\n- Implement max_size with LRU eviction to prevent memory exhaustion\n\n### Medium\n- Add cleanup for expired entries in get()\n- Handle non-hashable keys gracefully\n\n### Minor\n- Add docstrings and type hints\n- Consider dataclass for cache entries\n\n### Security\n- Unbounded cache creates DoS vector — fix with max_size\n\nGood approach overall. The caching pattern is sound but needs hardening before production use. Please address Critical and Medium items.",
        "mock_tokens_in": 2050,
        "mock_tokens_out": 1480,
    },
]

# ---------------------------------------------------------------------------
# OpenTelemetry setup
# ---------------------------------------------------------------------------

resource = Resource.create({
    "service.name": "agentrewind-demo-agent",
    "agent.run_id": RUN_ID,
})

provider = TracerProvider(resource=resource)
exporter = OTLPSpanExporter(endpoint=f"{OTEL_ENDPOINT}/v1/traces")
provider.add_span_processor(BatchSpanProcessor(exporter))
trace.set_tracer_provider(provider)
tracer = trace.get_tracer("agentrewind.demo.agent")

# ---------------------------------------------------------------------------
# LLM call helpers
# ---------------------------------------------------------------------------


def call_mock_llm(step: dict) -> dict:
    """Simulate an LLM call with sleep and hardcoded response."""
    sleep_time = 2.0 + (step["index"] * 0.3)
    time.sleep(sleep_time)
    return {
        "response": step["mock_response"],
        "tokens_in": step["mock_tokens_in"],
        "tokens_out": step["mock_tokens_out"],
        "model": "mock-gpt-4o-mini",
        "system": "mock",
    }


def call_real_llm(step: dict) -> dict:
    """Call OpenAI gpt-4o-mini with a real prompt."""
    from openai import OpenAI
    client = OpenAI()
    response = client.chat.completions.create(
        model="gpt-4o-mini",
        messages=[{"role": "user", "content": step["prompt"]}],
        max_tokens=2000,
    )
    usage = response.usage
    return {
        "response": response.choices[0].message.content,
        "tokens_in": usage.prompt_tokens,
        "tokens_out": usage.completion_tokens,
        "model": "gpt-4o-mini",
        "system": "openai",
    }


def call_llm(step: dict) -> dict:
    if USE_REAL_LLM:
        return call_real_llm(step)
    return call_mock_llm(step)


def calc_cost(tokens_in: int, tokens_out: int) -> float:
    return (tokens_in * PRICE_INPUT_PER_M / 1_000_000) + (tokens_out * PRICE_OUTPUT_PER_M / 1_000_000)

# ---------------------------------------------------------------------------
# Postgres helpers (write checkpoints + LLM metrics directly)
# ---------------------------------------------------------------------------


def get_pg_connection():
    if not POSTGRES_DSN:
        return None
    try:
        conn = psycopg2.connect(POSTGRES_DSN)
        conn.autocommit = True
        return conn
    except Exception as e:
        print(f"{YELLOW}WARNING: Could not connect to Postgres: {e}{RESET}")
        return None


def write_checkpoint(conn, step_index: int, step_name: str, trace_id: str, span_id: str, context: dict):
    if conn is None:
        return
    try:
        with conn.cursor() as cur:
            cur.execute(
                """INSERT INTO checkpoints (pod_name, namespace, step_index, step_name, trace_id, span_id, context)
                   VALUES (%s, %s, %s, %s, %s, %s, %s)""",
                (POD_NAME, POD_NAMESPACE, step_index, step_name, trace_id, span_id, json.dumps(context)),
            )
    except Exception as e:
        print(f"{YELLOW}WARNING: Failed to write checkpoint: {e}{RESET}")


def write_llm_metric(conn, step_index: int, step_name: str, model: str, tokens_in: int, tokens_out: int, latency_ms: float, cost_usd: float):
    if conn is None:
        return
    try:
        with conn.cursor() as cur:
            cur.execute(
                """INSERT INTO llm_metrics (run_id, pod_name, namespace, step_index, step_name, model, tokens_in, tokens_out, latency_ms, cost_usd)
                   VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s)""",
                (RUN_ID, POD_NAME, POD_NAMESPACE, step_index, step_name, model, tokens_in, tokens_out, latency_ms, cost_usd),
            )
    except Exception as e:
        print(f"{YELLOW}WARNING: Failed to write LLM metric: {e}{RESET}")

# ---------------------------------------------------------------------------
# Recovery: read Downward API annotations
# ---------------------------------------------------------------------------


def read_recovery_checkpoint() -> dict | None:
    """Read recovery annotation from Kubernetes Downward API projected volume."""
    if not os.path.exists(ANNOTATIONS_PATH):
        return None
    try:
        with open(ANNOTATIONS_PATH) as f:
            content = f.read()
        for line in content.split("\n"):
            if line.startswith('agentrewind.io/last-checkpoint="'):
                value = line.split("=", 1)[1]
                # Downward API wraps values in quotes and escapes inner quotes
                value = value.strip('"').replace('\\"', '"')
                return json.loads(value)
    except Exception as e:
        print(f"{YELLOW}WARNING: Failed to read recovery annotation: {e}{RESET}")
    return None

# ---------------------------------------------------------------------------
# LangGraph pipeline
# ---------------------------------------------------------------------------


class AgentState(TypedDict):
    step_index: int
    results: dict[str, Any]
    cumulative_cost: float
    trace_id: str
    skipped_steps: int
    skipped_cost: float
    recovery_step: int


def make_step_fn(step: dict, pg_conn):
    """Create a LangGraph node function for a pipeline step."""
    def step_fn(state: AgentState) -> AgentState:
        idx = step["index"]
        name = step["name"]

        # Skip if already completed (recovery)
        if idx <= state.get("recovery_step", -1):
            estimated_cost = calc_cost(step["mock_tokens_in"], step["mock_tokens_out"])
            print(f"  {CYAN}⏭  Step {idx}: {name} — SKIPPED (already completed){RESET}")
            return {
                **state,
                "step_index": idx,
                "skipped_steps": state.get("skipped_steps", 0) + 1,
                "skipped_cost": state.get("skipped_cost", 0.0) + estimated_cost,
            }

        start = time.time()
        with tracer.start_as_current_span(f"agent.step.{name}") as span:
            result = call_llm(step)
            elapsed_ms = (time.time() - start) * 1000
            cost = calc_cost(result["tokens_in"], result["tokens_out"])
            cumulative = state.get("cumulative_cost", 0.0) + cost

            # Set span attributes
            span.set_attribute("gen_ai.system", result["system"])
            span.set_attribute("gen_ai.request.model", result["model"])
            span.set_attribute("gen_ai.usage.input_tokens", result["tokens_in"])
            span.set_attribute("gen_ai.usage.output_tokens", result["tokens_out"])
            span.set_attribute("gen_ai.latency_ms", elapsed_ms)
            span.set_attribute("agent.step_index", idx)
            span.set_attribute("agent.step_name", name)
            span.set_attribute("agent.step_cost_usd", cost)
            span.set_attribute("agent.cumulative_cost_usd", cumulative)
            span.set_attribute("agent.run_id", RUN_ID)
            span.set_attribute("agent.total_steps", 5)

            trace_id = format(span.get_span_context().trace_id, "032x")
            span_id = format(span.get_span_context().span_id, "016x")

            # Write checkpoint to Postgres
            write_checkpoint(pg_conn, idx, name, trace_id, span_id, {
                "step_index": idx,
                "step_name": name,
                "run_id": RUN_ID,
                "cumulative_cost_usd": cumulative,
            })

            # Write LLM metric to Postgres
            write_llm_metric(pg_conn, idx, name, result["model"],
                             result["tokens_in"], result["tokens_out"],
                             elapsed_ms, cost)

            print(
                f"  {GREEN}✓  Step {idx}: {name}{RESET}"
                f"  — {result['tokens_in']} in / {result['tokens_out']} out"
                f"  — ${cost:.4f} (total: ${cumulative:.4f})"
                f"  — {elapsed_ms:.0f}ms"
            )

            # Signal completion for kill script detection
            print(f"Step {idx} complete: {name}")

            return {
                **state,
                "step_index": idx,
                "results": {**state.get("results", {}), name: result["response"]},
                "cumulative_cost": cumulative,
                "trace_id": trace_id,
            }
    return step_fn

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


def main():
    print(f"\n{BOLD}{'='*60}{RESET}")
    print(f"{BOLD}  AgentRewind Demo — PR Review Pipeline{RESET}")
    print(f"{BOLD}{'='*60}{RESET}")
    print(f"  Run ID:    {RUN_ID}")
    print(f"  Pod:       {POD_NAME}")
    print(f"  Namespace: {POD_NAMESPACE}")
    print(f"  LLM Mode:  {'🔴 REAL (OpenAI gpt-4o-mini)' if USE_REAL_LLM else '🟢 MOCK (deterministic)'}")
    print(f"{'='*60}\n")

    # Check for recovery checkpoint
    recovery = read_recovery_checkpoint()
    recovery_step = -1
    if recovery:
        recovery_step = recovery.get("step_index", -1)
        estimated_saved = sum(
            calc_cost(s["mock_tokens_in"], s["mock_tokens_out"])
            for s in STEPS if s["index"] <= recovery_step
        )
        print(f"  {BOLD}{CYAN}🔄 RECOVERED from step {recovery_step} ({recovery.get('step_name', '?')}){RESET}")
        print(f"  {CYAN}   Skipping ${estimated_saved:.4f} in LLM costs{RESET}")
        print()
    else:
        print(f"  No recovery checkpoint found — starting from step 0\n")

    # Connect to Postgres
    pg_conn = get_pg_connection()

    # Build LangGraph pipeline
    builder = StateGraph(AgentState)
    for step in STEPS:
        builder.add_node(step["name"], make_step_fn(step, pg_conn))

    # Chain: fetch_pr -> analyze_diff -> check_style -> security_scan -> write_review
    builder.set_entry_point("fetch_pr")
    for i in range(len(STEPS) - 1):
        builder.add_edge(STEPS[i]["name"], STEPS[i + 1]["name"])
    builder.add_edge(STEPS[-1]["name"], END)

    graph = builder.compile()

    # Run the pipeline
    initial_state: AgentState = {
        "step_index": -1,
        "results": {},
        "cumulative_cost": 0.0,
        "trace_id": "",
        "skipped_steps": 0,
        "skipped_cost": 0.0,
        "recovery_step": recovery_step,
    }

    print(f"  {BOLD}Running pipeline...{RESET}\n")
    final_state = graph.invoke(initial_state)

    # Flush OTel spans
    provider.force_flush()

    # Print summary
    total_steps = 5
    skipped = final_state.get("skipped_steps", 0)
    executed = total_steps - skipped
    total_cost = final_state.get("cumulative_cost", 0.0)
    saved_cost = final_state.get("skipped_cost", 0.0)

    print(f"\n{BOLD}{'='*60}{RESET}")
    print(f"{BOLD}  Pipeline Complete!{RESET}")
    print(f"{'='*60}")
    print(f"  Steps executed: {executed}/{total_steps}")
    print(f"  Steps skipped:  {skipped}/{total_steps}")
    print(f"  Total cost:     ${total_cost:.4f}")
    if saved_cost > 0:
        print(f"  {GREEN}Cost saved:     ${saved_cost:.4f} (by recovery){RESET}")
    print(f"  Run ID:         {RUN_ID}")
    print(f"  Trace ID:       {final_state.get('trace_id', 'N/A')}")
    print(f"  Jaeger URL:     http://localhost:16686/trace/{final_state.get('trace_id', '')}")
    print(f"{'='*60}\n")

    # Close Postgres connection
    if pg_conn:
        pg_conn.close()


if __name__ == "__main__":
    main()
