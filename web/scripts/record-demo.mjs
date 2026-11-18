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
const demoEmail = process.env.DEMO_EMAIL || 'demo@o1.work.gd';
const demoPassword = process.env.DEMO_PASSWORD;
if (!demoPassword) {
	throw new Error('DEMO_PASSWORD is required (the demo owner account)');
}
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

// A visible pointer and click ripple so the recording shows the interaction.
await context.addInitScript(() => {
	const install = () => {
		if (document.getElementById('__demo_pointer')) return;
		const style = document.createElement('style');
		style.textContent = `
			#__demo_pointer { position: fixed; top: 0; left: 0; width: 20px; height: 20px;
				margin: -10px 0 0 -10px; border-radius: 50%;
				background: rgba(34,197,94,.92); border: 2px solid #fff;
				box-shadow: 0 0 0 4px rgba(34,197,94,.28), 0 2px 6px rgba(0,0,0,.3);
				pointer-events: none; z-index: 2147483647;
				transition: transform 80ms linear; }
			#__demo_pointer::after { content: ''; position: absolute; inset: -8px;
				border-radius: 50%; border: 2px solid rgba(34,197,94,.7); opacity: 0; }
			#__demo_pointer.click::after { animation: __demo_ripple 420ms ease-out; }
			@keyframes __demo_ripple { from { transform: scale(.4); opacity: .9; }
				to { transform: scale(1.7); opacity: 0; } }
		`;
		document.head.appendChild(style);

		const dot = document.createElement('div');
		dot.id = '__demo_pointer';
		dot.style.transform = 'translate(640px, 360px)';
		document.documentElement.appendChild(dot);

		window.addEventListener(
			'mousemove',
			(event) => {
				dot.style.transform = `translate(${event.clientX}px, ${event.clientY}px)`;
			},
			true,
		);
		window.addEventListener(
			'mousedown',
			() => {
				dot.classList.add('click');
				setTimeout(() => dot.classList.remove('click'), 450);
			},
			true,
		);
	};
	if (document.readyState === 'loading') {
		document.addEventListener('DOMContentLoaded', install, { once: true });
	} else {
		install();
	}
});

const page = await context.newPage();
const video = page.video();
const pause = (ms) => page.waitForTimeout(ms);

// Wheel in small steps so scrolling reads as motion instead of a jump.
async function smoothScroll(delta, steps = 10) {
	for (let i = 0; i < steps; i += 1) {
		await page.mouse.wheel(0, delta / steps);
		await page.waitForTimeout(70);
	}
}

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
await page.mouse.move(640, 360, { steps: 8 });
await smoothScroll(320, 12);
await pause(1200);
await smoothScroll(-320, 12);
await pause(800);

// 2. Sign in with the demo owner account (billing needs an admin).
await page.goto(`${base}/login`, { waitUntil: 'networkidle' });
await pause(1500);
await page.getByLabel('Email').fill(demoEmail);
await page.getByLabel('Password').fill(demoPassword);
await pause(500);
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
await page.getByText('GitHub API', { exact: true }).first().waitFor();
await page.mouse.move(520, 320, { steps: 15 });
await pause(2200);

// 5. Billing and plan limits.
await page.goto(`${base}/w/demo/settings/billing`, {
	waitUntil: 'networkidle',
});
await page.getByText('Team').waitFor();
await page.mouse.move(360, 260, { steps: 12 });
await pause(3000);

// 6. Back to the public status page.
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
