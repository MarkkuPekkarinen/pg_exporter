\set query_band random(1, 4)
\if :query_band = 1
  \set query_ms 300
\elif :query_band = 2
  \set query_ms 3000
\elif :query_band = 3
  \set query_ms 10000
\else
  \set query_ms 30000
\endif

\set hold_band random(1, 4)
\if :hold_band = 1
  \set hold_ms 300
\elif :hold_band = 2
  \set hold_ms 3000
\elif :hold_band = 3
  \set hold_ms 10000
\else
  \set hold_ms 30000
\endif

BEGIN;
SELECT pg_current_xact_id(), pg_sleep(:query_ms / 1000.0);
\sleep :hold_ms ms
COMMIT;
