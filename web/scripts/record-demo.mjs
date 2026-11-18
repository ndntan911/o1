// Records the public demo tour for the portfolio: status page, demo login,
// monitor detail, and an incident. Writes WebM to docs/portfolio/demo.webm;
// convert it with the command in the file header.
//
//   npm run demo:video
//   ffmpeg -i ../docs/portfolio/demo.webm -c:v libx264 -pix_fmt yuv420p \
//     -crf 26 -movflags +faststart ../docs/portfolio/demo.mp4
import { chromium } from 'playwright';
import { mkdir } from 'node:fs/promises';
import { resolve } from 'node:path';

const base = process.env.DEMO_BASE_URL || 'https://o1.work.gd';
const outDir = resolve(import.meta.dirname, '../../docs/portfolio');
const videoDir = resolve(import.meta.dirname, '../../.video-tmp');
await mkdir(outDir, { recursive: true });
await mkdir(videoDir, { recursive: true });

const monitorId = '17966bb9-6010-4310-bcaa-007c0555c6fc';

const browser = await chromium.launch();
const context = await browser.newContext({
	viewport: { width: 1280, height: 720 },
	recordVideo: { dir: videoDir, size: { width: 1280, height: 720 } },
});

// A visible pointer so the recording shows where the interaction happens.
await context.addInitScript(() => {
	const dot = document.createElement('div');
	dot.id = '__demo_pointer';
	Object.assign(dot.style, {
		position: 'fixed',
		width: '18px',
		height: '18px',
		borderRadius: '50%',
		background: 'rgba(34,197,94,0.85)',
		border: '2px solid white',
		boxShadow: '0 0 0 3px rgba(34,197,94,0.25)',
		pointerEvents: 'none',
		zIndex: '2147483647',
		transform: 'translate(-50%,-50%)',
		transition: 'transform 120ms ease-out',
		inset: '0 auto auto 0',
	});
	document.documentElement.appendChild(dot);
	window.addEventListener('mousemove', (event) => {
		dot.style.transform = `translate(${event.clientX - 9}px, ${event.clientY - 9}px)`;
	});
});

const page = await context.newPage();
const video = page.video();
const pause = (ms) => page.waitForTimeout(ms);

async function moveAndClick(locator) {
	const box = await locator.boundingBox();
	if (box) {
		await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2, {
			steps: 12,
		});
		await pause(250);
	}
	await locator.click();
}

// 1. Public status page.
await page.goto(`${base}/status/demo`, { waitUntil: 'networkidle' });
await pause(2500);
await page.mouse.wheel(0, 260);
await pause(1200);
await page.mouse.wheel(0, -260);
await pause(800);

// 2. Sign in with the seeded read-only demo account.
await page.goto(`${base}/login`, { waitUntil: 'networkidle' });
await pause(1500);
await moveAndClick(page.getByRole('button', { name: /use demo account/i }));
await pause(900);
await moveAndClick(page.getByRole('button', { name: 'Sign in' }));
await page.waitForURL(/\/w\//, { timeout: 20000 });
await pause(2500);

// 3. Monitor list and one monitor's latency detail.
await page.mouse.move(420, 260, { steps: 10 });
await pause(900);
await page.goto(`${base}/w/demo/monitors/${monitorId}?window=24h`, {
	waitUntil: 'networkidle',
});
await page.getByText('Loading results…').waitFor({ state: 'detached' });
await page.getByText('Loading chart…').waitFor({ state: 'detached' });
await pause(2500);

// 4. Incident history.
await page.goto(`${base}/w/demo/incidents`, { waitUntil: 'networkidle' });
await pause(1500);
await page.getByText('All', { exact: true }).click();
await pause(1800);
await page.mouse.move(520, 320, { steps: 15 });
await pause(1800);

// 5. Back to the public status page.
await page.goto(`${base}/status/demo`, { waitUntil: 'networkidle' });
await pause(1800);

await context.close();
const webmPath = await video.path();
const target = resolve(outDir, 'demo.webm');
await import('node:fs/promises').then(({ rename }) => rename(webmPath, target));
await browser.close();
await import('node:fs/promises').then(({ rm }) =>
	rm(videoDir, { recursive: true, force: true }),
);
console.log('wrote', target);
