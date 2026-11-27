/// <reference types="node" />
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { describe, expect, test } from 'vitest';

const read = (path: string) =>
	readFile(fileURLToPath(new URL(path, import.meta.url)), 'utf8');

describe('portfolio landing page', () => {
	test('ships complete search metadata and the demo CTAs', async () => {
		const html = await read('../landing.html');
		const doc = new DOMParser().parseFromString(html, 'text/html');

		expect(doc.title).toBe(
			'o1 — uptime and incident monitoring for small teams',
		);

		const description =
			doc.querySelector('meta[name="description"]')?.getAttribute('content') ??
			'';
		expect(description.length).toBeGreaterThanOrEqual(120);
		expect(description.length).toBeLessThanOrEqual(170);
		expect(description).toContain('uptime');

		expect(
			doc.querySelector('link[rel="canonical"]')?.getAttribute('href'),
		).toBe('https://o1.work.gd/');
		expect(
			doc.querySelector('meta[property="og:image"]')?.getAttribute('content'),
		).toBe('https://o1.work.gd/og.png');
		expect(
			doc.querySelector('meta[name="twitter:card"]')?.getAttribute('content'),
		).toBe('summary_large_image');
		expect(doc.querySelectorAll('h1')).toHaveLength(1);

		const demo = doc.querySelector('a[href="/login"]');
		expect(demo?.textContent).toContain('Open the live demo');
		expect(doc.querySelector('a[href="/status/demo"]')).not.toBeNull();
	});

	test('includes JSON-LD but no executable JavaScript', async () => {
		const html = await read('../landing.html');
		const doc = new DOMParser().parseFromString(html, 'text/html');

		const scripts = [...doc.querySelectorAll('script')];
		expect(scripts).toHaveLength(1);
		expect(scripts[0].getAttribute('type')).toBe('application/ld+json');

		const data = JSON.parse(scripts[0].textContent ?? '');
		expect(data['@type']).toBe('SoftwareApplication');
		expect(data.name).toBe('o1');
		expect(data.codeRepository).toBe('https://github.com/ndntan911/o1');
	});

	test('every image has alt text and intrinsic dimensions', async () => {
		const html = await read('../landing.html');
		const doc = new DOMParser().parseFromString(html, 'text/html');
		const images = [...doc.querySelectorAll('img')];

		expect(images.length).toBeGreaterThan(0);
		for (const image of images) {
			expect(image.getAttribute('alt')?.trim()).not.toBe('');
			expect(image.getAttribute('width')).toMatch(/^\d+$/);
			expect(image.getAttribute('height')).toMatch(/^\d+$/);
		}
	});
});

describe('crawler files and static routing', () => {
	test('sitemap lists only the landing page and demo status page', async () => {
		const xml = await read('../public/sitemap.xml');
		const urls = [...xml.matchAll(/<loc>([^<]+)<\/loc>/g)].map(
			(match) => match[1],
		);

		expect(urls).toEqual([
			'https://o1.work.gd/',
			'https://o1.work.gd/status/demo',
		]);
	});

	test('robots.txt points at the sitemap and keeps app routes out', async () => {
		const robots = await read('../public/robots.txt');

		expect(robots).toContain('Sitemap: https://o1.work.gd/sitemap.xml');
		expect(robots).toContain('Allow: /status/');
		for (const path of ['/app', '/w/', '/workspaces', '/api/']) {
			expect(robots).toContain(`Disallow: ${path}`);
		}
	});

	test('Caddy serves the landing page and a real sitemap', async () => {
		const caddy = await read('../Caddyfile');

		expect(caddy).toMatch(/rewrite \* \/landing\.html/);
		expect(caddy).toMatch(/redir @landing-file \/ 301/);
		expect(caddy).toMatch(/handle \/sitemap\.xml/);
		expect(caddy).toContain('X-Robots-Tag "noindex"');
		expect(caddy).not.toMatch(/@app path[^\n]*\/status/);
	});
});
