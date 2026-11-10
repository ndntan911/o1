import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';
import type { Breadcrumb, Event } from '@sentry/react';

const sentryInit = vi.hoisted(() => vi.fn());

vi.mock('@sentry/react', () => ({
	init: sentryInit,
}));

import { initSentry, scrubSentryBreadcrumb, scrubSentryEvent } from './sentry';

beforeEach(() => {
	sentryInit.mockClear();
});

afterEach(() => {
	vi.unstubAllEnvs();
});

describe('initSentry', () => {
	test('does nothing when VITE_SENTRY_DSN is not configured', () => {
		vi.stubEnv('VITE_SENTRY_DSN', '');

		expect(initSentry()).toBe(false);
		expect(sentryInit).not.toHaveBeenCalled();
	});

	test('initializes Sentry with redaction and no tracing when a DSN is configured', () => {
		vi.stubEnv('VITE_SENTRY_DSN', 'https://public@example.com/1');
		vi.stubEnv('VITE_RELEASE', 'v1.2.3');

		expect(initSentry()).toBe(true);
		expect(sentryInit).toHaveBeenCalledTimes(1);

		const options = sentryInit.mock.calls[0][0] as {
			dsn: string;
			environment: string;
			release: string;
			tracesSampleRate: number;
			dataCollection: { urlQueryParams: boolean };
			beforeSend: unknown;
			beforeBreadcrumb: unknown;
		};
		expect(options.dsn).toBe('https://public@example.com/1');
		expect(options.environment).toBe(import.meta.env.MODE);
		expect(options.release).toBe('v1.2.3');
		expect(options.tracesSampleRate).toBe(0);
		expect(options.dataCollection).toEqual({ urlQueryParams: false });
		expect(typeof options.beforeSend).toBe('function');
		expect(typeof options.beforeBreadcrumb).toBe('function');
	});
});

describe('scrubSentryBreadcrumb', () => {
	test('strips query strings and fragments from url, from, and to', () => {
		const breadcrumb: Breadcrumb = {
			category: 'navigation',
			data: {
				from: '/workspaces/1/monitors?token=secret#section',
				to: '/workspaces/1/monitors/2?email=alice%40example.com',
				url: 'https://app.example.com/api/monitors?token=secret',
				method: 'GET',
			},
		};

		const scrubbed = scrubSentryBreadcrumb(breadcrumb);

		expect(scrubbed.data).toEqual({
			from: '/workspaces/1/monitors',
			to: '/workspaces/1/monitors/2',
			url: 'https://app.example.com/api/monitors',
			method: 'GET',
		});
	});

	test('leaves breadcrumbs without data untouched', () => {
		const breadcrumb: Breadcrumb = { message: 'ui.click' };

		expect(scrubSentryBreadcrumb(breadcrumb)).toEqual(breadcrumb);
	});
});

describe('scrubSentryEvent', () => {
	test('removes request payloads, headers, cookies, query strings, and user email', () => {
		const event: Event = {
			transaction: '/workspaces/$workspaceId/monitors',
			request: {
				method: 'POST',
				url: 'https://app.example.com/api/monitors?token=secret',
				data: { password: 'hunter2' },
				headers: { Authorization: 'Bearer secret' },
				cookies: { session: 'abc123' },
				query_string: 'token=secret',
				env: { REMOTE_ADDR: '203.0.113.7' },
			},
			user: { id: 'user-1', email: 'person@example.com' },
		};

		const scrubbed = scrubSentryEvent(event);

		expect(scrubbed.request?.method).toBe('POST');
		expect(scrubbed.request?.url).toBe('https://app.example.com/api/monitors');
		expect(scrubbed.request?.data).toBeUndefined();
		expect(scrubbed.request?.headers).toBeUndefined();
		expect(scrubbed.request?.cookies).toBeUndefined();
		expect(scrubbed.request?.query_string).toBeUndefined();
		expect(scrubbed.request?.env).toBeUndefined();
		expect(scrubbed.user).toEqual({ id: 'user-1' });
		expect(scrubbed.transaction).toBe('/workspaces/$workspaceId/monitors');
	});

	test('leaves events without request or user data untouched', () => {
		const event: Event = { message: 'boom' };

		expect(scrubSentryEvent(event)).toEqual(event);
	});

	test('scrubs URLs in event breadcrumbs', () => {
		const event: Event = {
			breadcrumbs: [
				{
					category: 'navigation',
					data: { from: '/a?token=1', to: '/b#secret' },
				},
			],
		};

		const scrubbed = scrubSentryEvent(event);

		expect(scrubbed.breadcrumbs?.[0].data).toEqual({
			from: '/a',
			to: '/b',
		});
	});
});
