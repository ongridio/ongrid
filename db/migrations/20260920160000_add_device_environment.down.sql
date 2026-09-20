-- Roll back Manager first. Export newly assigned device environments before
-- dropping this column; legacy Edge configuration remains intact.
ALTER TABLE devices DROP COLUMN environment;
