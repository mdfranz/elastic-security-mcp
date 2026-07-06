import asyncio
import logging
import os
import shutil
import sys
import tempfile
import time
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

from pydantic_ai import Agent, RunContext
from pydantic_ai.capabilities.hooks import Hooks
from pydantic_ai.mcp import MCPToolset, StdioTransport
from pydantic_ai.models import ModelRequestContext, ModelResponse


def setup_logging(log_file: str, debug_libs: tuple[str, ...]) -> None:
    logging.basicConfig(
        level=logging.DEBUG,
        format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S",
        handlers=[logging.FileHandler(log_file), logging.StreamHandler()],
    )
    # Console at INFO; file stays at DEBUG. `type(h) is` (not isinstance) avoids matching FileHandler.
    for h in logging.root.handlers:
        if type(h) is logging.StreamHandler:
            h.setLevel(logging.INFO)
    for name in debug_libs:
        logging.getLogger(name).setLevel(logging.DEBUG)


setup_logging("pydantic_ai_test.log", ("mcp", "httpx", "pydantic_ai"))
logger = logging.getLogger("pydantic_ai_test")

REPO_ROOT = Path(__file__).resolve().parent.parent
SERVER_BINARY = "elastic-mcp-server"

# (bare_model_prefixes, provider_prefix_to_apply, api_key_envs)
PROVIDERS = [
    (("gpt-", "o1-"), "openai:", ("OPENAI_API_KEY",)),
    (("gemini-",), "google:", ("GOOGLE_API_KEY", "GEMINI_API_KEY")),
    (("claude-",), "anthropic:", ("ANTHROPIC_API_KEY",)),
]


def resolve_model(requested: str) -> str | None:
    """Add a provider prefix if missing and verify the API key. Returns None if a key is missing."""
    name = requested
    if ":" not in name:
        for prefixes, provider, _ in PROVIDERS:
            if name.startswith(prefixes):
                name = provider + name
                break

    for _, provider, env_keys in PROVIDERS:
        if name.startswith(provider):
            if not any(os.getenv(k) for k in env_keys):
                logger.error(f"Error: {' or '.join(env_keys)} not found in environment.")
                return None
            break

    return name


def resolve_server_path() -> str | None:
    """Find the elastic-mcp-server binary: PATH first, then the repo root (`make build` output)."""
    found = shutil.which(SERVER_BINARY)
    if found:
        return found

    local = REPO_ROOT / SERVER_BINARY
    if local.is_file() and os.access(local, os.X_OK):
        return str(local)

    logger.error(
        f"Error: {SERVER_BINARY} not found in PATH or at {local}. "
        f"Build it first with `make build` (or `go build -o {SERVER_BINARY} ./cmd/server/main.go`)."
    )
    return None


def _total_tokens(usage: Any) -> int:
    t = getattr(usage, "total_tokens", 0)
    return t() if callable(t) else (t or 0)


@dataclass
class TokenUsageTotals:
    requests: int = 0
    input_tokens: int = 0
    output_tokens: int = 0
    total_tokens: int = 0
    cache_read_tokens: int = 0

    def add(self, usage: Any) -> None:
        self.requests += getattr(usage, "requests", 0) or 0
        self.input_tokens += getattr(usage, "input_tokens", 0) or 0
        self.output_tokens += getattr(usage, "output_tokens", 0) or 0
        self.total_tokens += _total_tokens(usage)
        self.cache_read_tokens += getattr(usage, "cache_read_tokens", 0) or 0

    def summary(self) -> str:
        return (
            f"requests={self.requests}, input={self.input_tokens}, "
            f"output={self.output_tokens}, total={self.total_tokens}, "
            f"cache_read={self.cache_read_tokens}"
        )


@dataclass
class RunStats:
    task_tokens: TokenUsageTotals = field(default_factory=TokenUsageTotals)
    overall_tokens: TokenUsageTotals = field(default_factory=TokenUsageTotals)

    def reset_task(self) -> None:
        self.task_tokens = TokenUsageTotals()

    def update(self, usage: Any) -> None:
        self.task_tokens.add(usage)
        self.overall_tokens.add(usage)


TASKS = [
    (
        "Structured discovery",
        """
        Inventory this Elasticsearch deployment before investigating anything.
        Call cluster_health to check overall cluster status, then list_indices to see
        what security-relevant indices exist (try patterns like logs-* or *suricata* or
        *zeek* if that narrows things usefully).
        Summarize the cluster health and what kinds of data are available to investigate.
        """,
    ),
    (
        "IP enrichment and event lookup",
        """
        Investigate the IP address 8.8.8.8.
        First use lookup_ip for a fast, cached history lookup on this IP.
        Then use search_security_events (try index logs-zeek.*-* or logs-suricata.*-*)
        filtering on this IP to find recent network activity involving it.
        Summarize what lookup_ip returned versus what the live event search found, and
        note whether the tool result was served from cache.
        """,
    ),
    (
        "Alert triage and process pivot",
        """
        Use search_security_alerts to find recent high or critical severity alerts.
        Pick one alert that references a host or process, then use search_processes to
        pivot and find related process activity on that host (by host, process_name, or
        parent_name — whichever the alert gives you).
        Summarize the alert and the process context you found, and mention which tools
        you used and why.
        """,
    ),
]


def build_export_task() -> tuple[str, str]:
    """Build the TLS export task with a fresh one-hour window ending now."""
    end = datetime.now(timezone.utc)
    start = end - timedelta(hours=1)
    start_s = start.strftime("%Y-%m-%dT%H:%M:%SZ")
    end_s = end.strftime("%Y-%m-%dT%H:%M:%SZ")
    filepath = str(Path(tempfile.gettempdir()) / f"tls_export_{end.strftime('%Y%m%d_%H%M%S')}.jsonl")

    return (
        "TLS export (last hour)",
        f"""
        Export the last hour of TLS/SSL session logs for offline review.
        Use export_security_events against index logs-zeek.ssl-* (fall back to
        logs-network_traffic.tls-* if that pattern has no data), with start={start_s}
        and end={end_s} — exactly one hour, no wider — and filepath={filepath}.
        Leave max_file_mb at its default; this is a narrow one-hour window so it should
        fit in a single file.
        After the export completes, report how many events were exported, how many
        files were written, and the exact file path(s) returned by the tool.
        """,
    )


SYSTEM_PROMPT = """
You are an Elastic Security detection engineer. Use the available MCP tools to
investigate Elasticsearch security data.

Prefer the narrow, fast-path tools when they fit: lookup_ip and lookup_domain for
cached history lookups, search_processes for endpoint process activity, and
search_security_alerts for detection alerts. Use search_security_events for
broader network/endpoint event queries, and search_elastic only when nothing
else fits. Tool results prefixed with a cache indicator mean the answer was
served from (or written to) cache rather than freshly queried.

CRITICAL: After using tools, always provide a detailed final answer that includes:
1. Summary of what you discovered or found
2. Which specific tools you used and why each one
3. Key findings from the data (include sample rows if relevant)
4. Your analysis and conclusions

Do not respond with blank or minimal text. Provide comprehensive explanations.
"""


async def run_pydantic_ai_mcp(requested_model: str):
    server_path = resolve_server_path()
    if not server_path:
        return

    if not os.getenv("ELASTIC_URL") or not os.getenv("ELASTIC_KEY"):
        logger.error("Error: ELASTIC_URL and ELASTIC_KEY must be set in the environment.")
        return

    model_name = resolve_model(requested_model)
    if model_name is None:
        return

    logger.info(f"Using model: {model_name}")
    logger.info(f"Using server: {server_path}")

    hooks = Hooks()

    @hooks.on.after_model_request
    async def log_model_usage(
        ctx: RunContext[None],
        *,
        request_context: ModelRequestContext,
        response: ModelResponse,
    ) -> ModelResponse:
        usage = response.usage
        logger.info(
            f"[Agent] Model turn finished | Model: {response.model_name} | Tokens: "
            f"input={usage.input_tokens}, output={usage.output_tokens}, "
            f"total={_total_tokens(usage)}, cache_read={usage.cache_read_tokens}"
        )
        return response

    stats = RunStats()

    # mcp's stdio_client only forwards an allowlisted subset of the parent env by
    # default (PATH, HOME, etc.) — ELASTIC_URL/ELASTIC_KEY/KIBANA_* would be silently
    # dropped, so forward the full environment explicitly.
    transport = StdioTransport(command=server_path, args=[], env=dict(os.environ))
    async with MCPToolset(transport, max_retries=3) as server:
        agent = Agent(
            model_name,
            toolsets=[server],
            capabilities=[hooks],
            system_prompt=SYSTEM_PROMPT,
        )

        tasks = [*TASKS, build_export_task()]

        try:
            for title, prompt in tasks:
                stats.reset_task()
                start = time.perf_counter()
                logger.info("\n" + "=" * 20)
                logger.info(f" TASK: {title}")
                logger.info("=" * 20)

                result = await agent.run(prompt)

                logger.info("\n[Final Answer]")
                logger.info(result.output)

                stats.update(result.usage)

                logger.info(f"[Token Totals] {stats.task_tokens.summary()}")
                elapsed_ms = (time.perf_counter() - start) * 1000
                logger.info(f"\n[elapsed: {elapsed_ms:.1f} ms]")

            logger.info(f"\n[Overall Token Totals] {stats.overall_tokens.summary()}")

        except Exception as e:
            logger.exception(f"An error occurred during agent execution: {e}")


if __name__ == "__main__":
    default_model = "claude-haiku-4-5"
    model = sys.argv[1] if len(sys.argv) > 1 else default_model
    asyncio.run(run_pydantic_ai_mcp(model))
