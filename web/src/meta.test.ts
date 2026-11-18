/// <reference types="node" />
import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { describe, expect, test } from 'vitest';

const projectFile = (path: string) =>
	fileURLToPath(new URL(path, import.meta.url));

const readProjectFile = (path: string) => readFile(projectFile(path), 'utf8');

// These tests guard the public surface of the deployed dashboard: link
// previews, crawler rules, and transport hardening. They read the source
// assets rather than rendered output because an SPA shell is copied verbatim
// into the image.
describe('web shell metadata', () => {
	test('describes the product for search engines and chat previews', async () => {
		const html = await readProjectFile('../index.html');

		// Attributes may be wrapped across lines by prettier, so match within a
		// single tag and allow whitespace between attributes.
		const hasMeta = (
			attribute: 'name' | 'property',
			key: string,
			value: string,
		) =>
			new RegExp(
				`<meta[^>]*\\s${attribute}="${key}"\\s+content="${value}"[^>]*>`,
			).test(html);

		expect(hasMeta('name', 'description', '[^"]{20,}')).toBe(true);
		expect(hasMeta('property', 'og:title', '[^"]+')).toBe(true);
		expect(hasMeta('property', 'og:description', '[^"]+')).toBe(true);
		expect(hasMeta('property', 'og:type', 'website')).toBe(true);
		expect(hasMeta('property', 'og:image', 'https://[^"]+/og\\.png')).toBe(
			true,
		);
		expect(hasMeta('name', 'twitter:card', 'summary_large_image')).toBe(true);
	});

	test('ships an OG image with the size crawlers expect', async () => {
		const png = await readFile(projectFile('../public/og.png'));

		expect(png.subarray(1, 4).toString()).toBe('PNG');
		expect(png.readUInt32BE(16)).toBe(1200);
		expect(png.readUInt32BE(20)).toBe(630);
	});

	test('serves a real robots.txt instead of the SPA shell', async () => {
		const robots = await readProjectFile('../public/robots.txt');

		expect(robots).toContain('User-agent: *');
		expect(robots).toContain('Disallow: /api/');
	});
});

describe('caddy hardening', () => {
	test('the CSP allows the inline theme script by hash', async () => {
		const html = await readProjectFile('../index.html');
		const inline = html.match(/<script>([\s\S]*?)<\/script>/)?.[1];
		expect(inline).toBeTruthy();

		const hash = createHash('sha256')
			.update(inline as string, 'utf8')
			.digest('base64');
		const caddyfile = await readProjectFile('../Caddyfile');

		expect(caddyfile).toContain('Content-Security-Policy');
		expect(caddyfile).toContain(`'sha256-${hash}'`);
		expect(caddyfile).toContain('{$O1_CSP_EXTRA_CONNECT}');
		expect(caddyfile).toContain("frame-ancestors 'none'");
	});

	test('sends a long-lived HSTS header and browser protections', async () => {
		const caddyfile = await readProjectFile('../Caddyfile');

		expect(caddyfile).toMatch(/Strict-Transport-Security "max-age=31536000"/);
		expect(caddyfile).toMatch(/X-Content-Type-Options "nosniff"/);
		expect(caddyfile).toMatch(/X-Frame-Options "DENY"/);
	});
});
