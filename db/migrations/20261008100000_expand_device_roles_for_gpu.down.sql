-- Roll back Manager first and remove GPU role assignments before running this
-- migration. The old constraint cannot accept the GPU bit.
ALTER TABLE devices DROP CHECK chk_devices_roles;
ALTER TABLE devices ADD CONSTRAINT chk_devices_roles CHECK (roles BETWEEN 0 AND 15);
