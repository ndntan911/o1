// Renders scripts/og-card.html to public/og.png for link previews. Run with
// `npm run og:image` after changing the card copy or design.
import { chromium } from 'playwright';
import { pathToFileURL } from 'node:url';
import { resolve } from 'node:path';

const browser = await chromium.launch();
const page = await browser.newPage({
	viewport: { width: 1200, height: 630 },
	deviceScaleFactor: 1,
});
await page.goto(
	pathToFileURL(resolve(import.meta.dirname, 'og-card.html')).href,
);
await page.evaluate(() => document.fonts.ready);
await page.waitForTimeout(300);
await page.screenshot({ path: 'public/og.png' });
await browser.close();
console.log('wrote public/og.png');
