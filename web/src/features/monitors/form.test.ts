import { describe, expect, test } from 'vitest';
import type { Monitor } from '@/lib/api/types';
import {
	buildCreatePayload,
	buildUpdatePayload,
	defaultMonitorValues,
	headerRowsToRecord,
	monitorToFormValues,
} from './form';

const monitorWithMaskedHeaders: Monitor = {
	id: 'm1',
	workspace_id: 'w1',
	name: 'API',
	url: 'https://api.example.com/health',
	method: 'POST',
	headers: { Authorization: '***', 'X-Api-Key': '***' },
	has_body: true,
	content_type: 'application/json',
	expected_status: '2xx',
	interval_seconds: 300,
	timeout_seconds: 10,
	confirmation_threshold: 2,
	recovery_threshold: 3,
	paused: false,
	status: 'up',
	last_check: null,
	summary_24h: null,
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z',
};

describe('defaultMonitorValues', () => {
	test('returns valid create defaults', () => {
		expect(defaultMonitorValues()).toEqual({
			name: '',
			url: '',
			method: 'GET',
			expected_status: '2xx',
			interval_seconds: 60,
			timeout_seconds: 10,
			confirmation_threshold: 2,
			recovery_threshold: 2,
			headers: [],
			body: '',
			content_type: '',
		});
	});
});

describe('monitorToFormValues', () => {
	test('never copies masked header values or the hidden body into form state', () => {
		const values = monitorToFormValues(monitorWithMaskedHeaders);

		expect(values.headers).toEqual([]);
		expect(values.body).toBe('');
		expect(values.name).toBe('API');
		expect(values.url).toBe('https://api.example.com/health');
		expect(values.method).toBe('POST');
		expect(values.expected_status).toBe('2xx');
		expect(values.interval_seconds).toBe(300);
		expect(values.timeout_seconds).toBe(10);
		expect(values.confirmation_threshold).toBe(2);
		expect(values.recovery_threshold).toBe(3);
		expect(JSON.stringify(values)).not.toContain('***');
	});
});

describe('headerRowsToRecord', () => {
	test('maps rows to a record and preserves empty values', () => {
		expect(
			headerRowsToRecord([
				{ name: 'Authorization', value: 'Bearer fresh' },
				{ name: 'X-Empty', value: '' },
			]),
		).toEqual({ Authorization: 'Bearer fresh', 'X-Empty': '' });
	});

	test('trims names, skips blank names, and lets the last duplicate win', () => {
		expect(
			headerRowsToRecord([
				{ name: ' X-Token ', value: 'one' },
				{ name: '   ', value: 'ignored' },
				{ name: 'X-Token', value: 'two' },
			]),
		).toEqual({ 'X-Token': 'two' });
	});
});

describe('buildCreatePayload', () => {
	test('includes every required field, headers, body, and content type', () => {
		const values = {
			...defaultMonitorValues(),
			name: 'API',
			url: 'https://api.example.com/health',
			method: 'POST' as const,
			headers: [{ name: 'Authorization', value: 'Bearer fresh' }],
			body: '{"ping":true}',
			content_type: 'application/json',
		};

		expect(buildCreatePayload(values)).toEqual({
			name: 'API',
			url: 'https://api.example.com/health',
			method: 'POST',
			headers: { Authorization: 'Bearer fresh' },
			body: '{"ping":true}',
			content_type: 'application/json',
			expected_status: '2xx',
			interval_seconds: 60,
			timeout_seconds: 10,
			confirmation_threshold: 2,
			recovery_threshold: 2,
		});
	});

	test('omits body and content type for GET and HEAD', () => {
		const values = {
			...defaultMonitorValues(),
			name: 'API',
			url: 'https://api.example.com/health',
			headers: [{ name: 'X-Token', value: 'abc' }],
			body: '{"ping":true}',
			content_type: 'application/json',
		};

		expect(buildCreatePayload({ ...values, method: 'GET' })).not.toHaveProperty(
			'body',
		);
		expect(buildCreatePayload({ ...values, method: 'GET' })).not.toHaveProperty(
			'content_type',
		);
		expect(
			buildCreatePayload({ ...values, method: 'HEAD' }),
		).not.toHaveProperty('body');
	});

	test('omits a content type when the body is empty', () => {
		const payload = buildCreatePayload({
			...defaultMonitorValues(),
			name: 'API',
			url: 'https://api.example.com/health',
			method: 'POST',
			content_type: 'application/json',
		});

		expect(payload).not.toHaveProperty('body');
		expect(payload).not.toHaveProperty('content_type');
	});
});

describe('buildUpdatePayload', () => {
	const initial = monitorToFormValues(monitorWithMaskedHeaders);
	const dirty = { name: true };

	test('edit omits headers and body unless explicitly replaced', () => {
		expect(
			buildUpdatePayload({ ...initial, name: 'Renamed' }, initial, dirty, {
				replaceHeaders: false,
				bodyMode: 'keep',
			}),
		).toEqual({ name: 'Renamed' });
	});

	test('edit never sends masked header values', () => {
		const payload = buildUpdatePayload(
			{
				...initial,
				headers: [{ name: 'Authorization', value: 'Bearer fresh' }],
			},
			initial,
			dirty,
			{ replaceHeaders: true, bodyMode: 'keep' },
		);

		expect(payload.headers).toEqual({ Authorization: 'Bearer fresh' });
		expect(JSON.stringify(payload)).not.toContain('***');
	});

	test('enabling replacement with zero rows clears all headers', () => {
		const payload = buildUpdatePayload(
			{ ...initial, headers: [] },
			initial,
			dirty,
			{ replaceHeaders: true, bodyMode: 'keep' },
		);

		expect(payload.headers).toEqual({});
	});

	test('whitespace-only header rows also clear all headers', () => {
		const payload = buildUpdatePayload(
			{ ...initial, headers: [{ name: '   ', value: '' }] },
			initial,
			{},
			{ replaceHeaders: true, bodyMode: 'keep' },
		);

		expect(payload.headers).toEqual({});
	});

	test('body keep omits body and content type; clear sends an empty body', () => {
		expect(
			buildUpdatePayload(
				initial,
				initial,
				{},
				{
					replaceHeaders: false,
					bodyMode: 'keep',
				},
			),
		).toEqual({});
		expect(
			buildUpdatePayload(
				initial,
				initial,
				{},
				{
					replaceHeaders: false,
					bodyMode: 'clear',
				},
			),
		).toEqual({ body: '' });
	});

	test('body replace sends the body and its content type', () => {
		const payload = buildUpdatePayload(
			{ ...initial, body: '{"ping":true}', content_type: 'application/json' },
			initial,
			{},
			{ replaceHeaders: false, bodyMode: 'replace' },
		);

		expect(payload).toEqual({
			body: '{"ping":true}',
			content_type: 'application/json',
		});
	});

	test('an empty replacement body clears the body and omits the content type', () => {
		const payload = buildUpdatePayload(
			{ ...initial, body: '', content_type: 'application/json' },
			initial,
			{},
			{ replaceHeaders: false, bodyMode: 'replace' },
		);

		expect(payload).toEqual({ body: '' });
	});

	test('a non-empty body sends an explicitly cleared content type', () => {
		const payload = buildUpdatePayload(
			{ ...initial, body: 'ping=true', content_type: '' },
			initial,
			{},
			{ replaceHeaders: false, bodyMode: 'replace' },
		);

		expect(payload).toEqual({ body: 'ping=true', content_type: '' });
	});

	test('never sends a non-empty body or content type for GET or HEAD', () => {
		const payload = buildUpdatePayload(
			{
				...initial,
				method: 'GET',
				body: 'ping=true',
				content_type: 'application/json',
			},
			initial,
			{ method: true },
			{ replaceHeaders: false, bodyMode: 'replace' },
		);

		expect(payload).toEqual({ method: 'GET' });
		expect(JSON.stringify(payload)).not.toContain('ping=true');
		expect(JSON.stringify(payload)).not.toContain('application/json');
	});

	test('only dirty plain fields are sent', () => {
		const payload = buildUpdatePayload(
			{
				...initial,
				name: 'Renamed',
				url: 'https://api.example.com/v2',
				method: 'PUT',
				interval_seconds: 600,
				timeout_seconds: 15,
				confirmation_threshold: 2,
				recovery_threshold: 3,
			},
			initial,
			{
				name: true,
				interval_seconds: true,
				confirmation_threshold: true,
			},
			{ replaceHeaders: false, bodyMode: 'keep' },
		);

		expect(payload).toEqual({
			name: 'Renamed',
			interval_seconds: 600,
			confirmation_threshold: 2,
		});
	});
});
