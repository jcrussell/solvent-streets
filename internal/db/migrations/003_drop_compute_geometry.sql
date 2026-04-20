-- compute_results.geometry_json existed only to feed a cosmetic map overlay
-- that duplicated the hex grid layer. Materializing the city-wide union was
-- the primary OOM contributor for large cities (see solvent-streets-dhi).
-- Drop the column; coverage is now represented entirely by hex_stats.
ALTER TABLE compute_results DROP COLUMN geometry_json;
