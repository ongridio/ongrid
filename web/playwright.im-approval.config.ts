import { defineConfig } from '@playwright/test';
export default defineConfig({
 testDir:'./e2e', testMatch:'im-approval.spec.ts', workers:1,
 outputDir:'../output/im-approval',
 use:{baseURL:'http://127.0.0.1:4179',channel:'chrome',headless:true},
 webServer:{command:'npm run preview -- --host 127.0.0.1 --port 4179',url:'http://127.0.0.1:4179',reuseExistingServer:!process.env.CI},
});
