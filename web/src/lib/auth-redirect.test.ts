import { expect, test } from 'vitest';
import { buildLoginSearch, shouldRedirectToLogin } from './auth-redirect';

test('public auth pages never trigger a login redirect', () => {
	expect(shouldRedirectToLogin('/login')).toBe(false);
	expect(shouldRedirectToLogin('/register')).toBe(false);
	expect(shouldRedirectToLogin('/verify-email')).toBe(false);
	expect(shouldRedirectToLogin('/forgot-password')).toBe(false);
	expect(shouldRedirectToLogin('/reset-password')).toBe(false);
	expect(buildLoginSearch('/login', '')).toEqual({});
});

test('app pages build a redirect target', () => {
	expect(shouldRedirectToLogin('/w/acme/monitors')).toBe(true);
	expect(buildLoginSearch('/w/acme/monitors', '?page=2')).toEqual({
		redirect: '/w/acme/monitors?page=2',
	});
});
