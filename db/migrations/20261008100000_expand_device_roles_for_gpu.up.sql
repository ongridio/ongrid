-- Apply before upgrading Manager. Existing role bits remain unchanged;
-- bit 4 (value 16) is reserved for the built-in GPU role.
ALTER TABLE devices DROP CHECK chk_devices_roles;
ALTER TABLE devices ADD CONSTRAINT chk_devices_roles CHECK (roles BETWEEN 0 AND 31);
