import * as Sentry from '@sentry/react';
import type { Event } from '@sentry/react';

/**
 * Removes URLs and paths from a URL, keeping the scheme, host, and route.
 * Query strings and fragments can contain tokens or other sensitive data, so
 * they must not leave the browser.
 */
function stripUrlSecrets(url: string): string {
	return url.split(/[?#]/, 1)[0];
}

/**
 * Redacts sensitive fields from an event before it is sent to Sentry.
 *
 * Request bodies, headers, cookies, query strings, environment data, and user
 * email addresses are removed. The HTTP method and route (the `transaction`
 * and the query-less request URL) are kept so errors stay actionable.
 */
export function scrubSentryEvent<T extends Event>(event: T): T {
	const scrubbed: Event = { ...event };

	if (event.request) {
		const request = { ...event.request };
		delete request.data;
		delete request.headers;
		delete request.cookies;
		delete request.query_string;
		delete request.env;
		if (request.url) {
			request.url = stripUrlSecrets(request.url);
		}
		scrubbed.request = request;
	}

	if (event.user) {
		const user = { ...event.user };
		delete user.email;
		scrubbed.user = user;
	}

	return scrubbed as T;
}

/**
 * Initializes the browser Sentry SDK when a DSN is configured.
 *
 * Tracing is disabled for the frontend; this integration only captures errors.
 * Returns whether Sentry was enabled for the current build.
 */
export function initSentry(): boolean {
	const dsn = import.meta.env.VITE_SENTRY_DSN;
	if (!dsn) {
		return false;
	}

	Sentry.init({
		dsn,
		environment: import.meta.env.MODE,
		release: import.meta.env.VITE_RELEASE || undefined,
		tracesSampleRate: 0,
		beforeSend: scrubSentryEvent,
	});

	return true;
}
