-- Generation latency and cost over the last 7 days, from generation_metrics (MySQL 8, window functions only).
--
-- Percentiles are nearest-rank: pN is the smallest value with at least N% of rows at or below it.
-- created_at is stored in UTC, so the window compares against UTC_TIMESTAMP().
-- total_ms runs from dequeue to end and excludes queue_wait_ms; add the two for what the merchant waited.
-- first_token_ms is 0 when the first model request never produced output, so those rows are left out of it.

-- 1. p50/p95 total_ms per model, succeeded generations only.
WITH ranked AS (
  SELECT model_id,
         total_ms,
         ROW_NUMBER() OVER (PARTITION BY model_id ORDER BY total_ms) AS rn,
         COUNT(*)     OVER (PARTITION BY model_id)                   AS n
  FROM generation_metrics
  WHERE created_at >= UTC_TIMESTAMP() - INTERVAL 7 DAY
    AND outcome = 'succeeded'
)
SELECT model_id,
       MAX(n)                                              AS generations,
       MIN(CASE WHEN rn >= CEIL(0.50 * n) THEN total_ms END) AS p50_total_ms,
       MIN(CASE WHEN rn >= CEIL(0.95 * n) THEN total_ms END) AS p95_total_ms
FROM ranked
GROUP BY model_id
ORDER BY generations DESC;

-- 2. p50/p95 first_token_ms per model (time to first real output of the turn's first model request).
WITH ranked AS (
  SELECT model_id,
         first_token_ms,
         ROW_NUMBER() OVER (PARTITION BY model_id ORDER BY first_token_ms) AS rn,
         COUNT(*)     OVER (PARTITION BY model_id)                         AS n
  FROM generation_metrics
  WHERE created_at >= UTC_TIMESTAMP() - INTERVAL 7 DAY
    AND first_token_ms > 0
)
SELECT model_id,
       MAX(n)                                                    AS generations,
       MIN(CASE WHEN rn >= CEIL(0.50 * n) THEN first_token_ms END) AS p50_first_token_ms,
       MIN(CASE WHEN rn >= CEIL(0.95 * n) THEN first_token_ms END) AS p95_first_token_ms
FROM ranked
GROUP BY model_id
ORDER BY generations DESC;

-- 3. Cost per model: total, average and p50/p95 per generation, every outcome (a failed turn still costs money).
-- cost_usd is NULL when the provider didn't report cost; those rows are counted but left out of the cost figures.
WITH ranked AS (
  SELECT model_id,
         cost_usd,
         ROW_NUMBER() OVER (PARTITION BY model_id ORDER BY cost_usd) AS rn,
         COUNT(*)     OVER (PARTITION BY model_id)                   AS n
  FROM generation_metrics
  WHERE created_at >= UTC_TIMESTAMP() - INTERVAL 7 DAY
    AND cost_usd IS NOT NULL
),
unreported AS (
  SELECT model_id, COUNT(*) AS generations_without_cost
  FROM generation_metrics
  WHERE created_at >= UTC_TIMESTAMP() - INTERVAL 7 DAY
    AND cost_usd IS NULL
  GROUP BY model_id
)
SELECT r.model_id,
       MAX(r.n)                                               AS generations_with_cost,
       COALESCE(MAX(u.generations_without_cost), 0)           AS generations_without_cost,
       SUM(r.cost_usd)                                        AS total_cost_usd,
       AVG(r.cost_usd)                                        AS avg_cost_usd,
       MIN(CASE WHEN r.rn >= CEIL(0.50 * r.n) THEN r.cost_usd END) AS p50_cost_usd,
       MIN(CASE WHEN r.rn >= CEIL(0.95 * r.n) THEN r.cost_usd END) AS p95_cost_usd
FROM ranked r
LEFT JOIN unreported u ON u.model_id <=> r.model_id
GROUP BY r.model_id
ORDER BY total_cost_usd DESC;

-- 4. p50/p95 preview_visible_ms per model: send pressed -> preview showing the result, measured by the browser.
-- Joined to generation_metrics for the model; only succeeded generations whose browser reported the moment.
WITH ranked AS (
  SELECT m.model_id,
         t.preview_visible_ms,
         ROW_NUMBER() OVER (PARTITION BY m.model_id ORDER BY t.preview_visible_ms) AS rn,
         COUNT(*)     OVER (PARTITION BY m.model_id)                               AS n
  FROM generation_client_timing t
  JOIN generation_metrics m ON m.generation_id = t.generation_id
  WHERE t.created_at >= UTC_TIMESTAMP() - INTERVAL 7 DAY
    AND m.outcome = 'succeeded'
    AND t.preview_visible_ms IS NOT NULL
)
SELECT model_id,
       MAX(n)                                                        AS generations,
       MIN(CASE WHEN rn >= CEIL(0.50 * n) THEN preview_visible_ms END) AS p50_preview_visible_ms,
       MIN(CASE WHEN rn >= CEIL(0.95 * n) THEN preview_visible_ms END) AS p95_preview_visible_ms
FROM ranked
GROUP BY model_id
ORDER BY generations DESC;
