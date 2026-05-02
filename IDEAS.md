# Efficiency Assessment & Optimization Ideas

## ✅ Completed Optimizations

### 1. Cache TTL Improvements (2026-05-02)
- **Change:** Increased cache TTLs for better hit rates
    - `list_indices`: 300s → 3600s (1 hour)
    - `search_elastic`: 60s → 600s (10 minutes)
    - `search_security_events`: 60s → 600s (10 minutes)
- **Impact:** Fewer redundant API calls to Elasticsearch, reducing latency and cost

### 2. Result Truncation Optimization (2026-05-02)
- **Change:** Reduced default max response characters from 50,000 to 20,000
- **Impact:** Keeps conversation history leaner, reduces cumulative token usage

### 3. Rolling Window History Pruning (2026-05-02)
- **Change:** Implemented `pruneHistory()` method with configurable max messages (15)
- **Before:** Hardcoded clearing of history on every input when memory disabled
- **After:** Rolling window keeps most recent messages while preserving system prompt
- **Impact:** Long-running sessions stay within token limits without losing context

## Future Optimization Ideas

### 1. Reactive Token Limit Handling
- **Consideration:** Add explicit token limit error detection to catch and gracefully handle `400: prompt is too long` responses.
- **Detail:** Implement a retry mechanism with automatic history pruning when LLM API returns token limit errors.

### 2. Adaptive History Pruning
- **Consideration:** Instead of fixed-size rolling window, prune based on message token count estimation.
- **Detail:** Keep history under a target token budget rather than a fixed message count.

### 3. Background Cache Warming
- **Consideration:** Pre-cache common queries and index patterns during idle time.
- **Detail:** Warm cache with frequent search patterns to improve response times.

### 4. Compression of Cached Results
- **Consideration:** Store compressed results in Redis to reduce memory footprint.
- **Detail:** Trade CPU for memory efficiency in high-volume scenarios.
