-- Apply before upgrading Manager. Startup migrates the preferred host Edge's
-- legacy APM default once; explicit empty values retain cluster inheritance.
ALTER TABLE devices ADD COLUMN environment VARCHAR(256) NULL, ALGORITHM=INSTANT;
