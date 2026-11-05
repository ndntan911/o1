import { expect, test } from 'vitest';
import { ApiError } from '@/lib/api/client';
import type { Me } from '@/lib/api/types';
import { findWorkspace, isUnauthenticated, safeRedirect } from './guards';

test('safeRedirect rejects external and protocol-relative paths', () => {
	expect(safeRedirect(undefined)).toBe('/');
	expect(safeRedirect('')).toBe('/');
	expect(safeRedirect('https://evil.example')).toBe('/');
	expect(safeRedirect('//evil.example')).toBe('/');
	expect(safeRedirect('/\\evil.example')).toBe('/');
	expect(safeRedirect('javascript:alert(1)')).toBe('/');
	expect(safeRedirect('/w/acme/monitors')).toBe('/w/acme/monitors');
	expect(safeRedirect('/')).toBe('/');
});

test('safeRedirect rejects control characters', () => {
	expect(safeRedirect('/w/acme\nmonitors')).toBe('/');
	expect(safeRedirect('/w/acme\u0000monitors')).toBe('/');
});

test('findWorkspace matches only workspace slugs', () => {
	const me: Me = {
		user: { id: 'u1', email: 'a@example.com', is_admin: false },
		workspaces: [{ id: 'w1', name: 'Acme', slug: 'acme', role: 'owner' }],
	};
	expect(findWorkspace(me, 'acme')?.id).toBe('w1');
	expect(findWorkspace(me, 'missing')).toBeUndefined();
	expect(findWorkspace(undefined, 'acme')).toBeUndefined();
});

test('isUnauthenticated matches only an unauthenticated problem', () => {
	const unauthenticated = new ApiError({
		type: 'about:blank',
		title: 'Authentication required',
		status: 401,
		code: 'unauthenticated',
	});
	const forbidden = new ApiError({
		type: 'about:blank',
		title: 'Forbidden',
		status: 403,
		code: 'forbidden',
	});
	expect(isUnauthenticated(unauthenticated)).toBe(true);
	expect(isUnauthenticated(forbidden)).toBe(false);
	expect(isUnauthenticated(new Error('boom'))).toBe(false);
	expect(isUnauthenticated(null)).toBe(false);
});
