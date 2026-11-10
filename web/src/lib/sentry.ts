import * as Sentry from '@sentry/react';
import type { Breadcrumb, Event } from '@sentry/react';

/**
 * Removes URLs and paths from a URL, keeping the scheme, host, and route.
 * Query strings and fragments can contain tokens or other sensitive data, so
 * they must not leave the browser.
 */
function stripUrlSecrets(url: string): string {
	return url.split(/[?#]/, 1)[0];
}

/**
 * Removes query strings and fragments from the URL fields Sentry attaches to
 * breadcrumbs: navigation breadcrumbs use `from` and `to`, while HTTP
 * breadcrumbs use `url`.
 */
export function scrubSentryBreadcrumb<T extends Breadcrumb>(breadcrumb: T): T {
	if (!breadcrumb.data) return breadcrumb;

	const data = { ...breadcrumb.data };
	for (const key of ['url', 'from', 'to'] as const) {
		const value = data[key];
		if (typeof value === 'string') {
			data[key] = stripUrlSecrets(value);
		}
	}
	return { ...breadcrumb, data };
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

	if (event.breadcrumbs) {
		scrubbed.breadcrumbs = event.breadcrumbs.map(scrubSentryBreadcrumb);
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
 * URL query parameters are disabled at collection time and breadcrumbs are
 * scrubbed as a second line of defense. Returns whether Sentry was enabled for
 * the current build.
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
		dataCollection: { urlQueryParams: false },
		beforeSend: scrubSentryEvent,
		beforeBreadcrumb: scrubSentryBreadcrumb,
	});

	return true;
}
