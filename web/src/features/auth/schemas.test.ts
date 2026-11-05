import { describe, expect, test } from 'vitest';
import { loginSchema, registerSchema, resetPasswordSchema } from './schemas';

describe('auth schemas', () => {
	test('trims and lowercases emails', () => {
		const parsed = loginSchema.parse({
			email: '  Alice@Example.COM ',
			password: 'x',
		});
		expect(parsed.email).toBe('alice@example.com');
	});

	test('register requires matching passwords of at least 12 characters', () => {
		const base = {
			email: 'a@example.com',
			password: 'short',
			confirmPassword: 'short',
		};
		expect(registerSchema.safeParse(base).success).toBe(false);
		expect(
			registerSchema.safeParse({
				email: 'a@example.com',
				password: 'correct horse battery staple',
				confirmPassword: 'different password value',
			}).success,
		).toBe(false);
	});

	test('reset requires matching passwords', () => {
		expect(
			resetPasswordSchema.safeParse({
				password: 'correct horse battery staple',
				confirmPassword: 'correct horse battery staple',
			}).success,
		).toBe(true);
		expect(
			resetPasswordSchema.safeParse({
				password: 'correct horse battery staple',
				confirmPassword: 'different password value',
			}).success,
		).toBe(false);
	});
});
