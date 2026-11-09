import { describe, expect, test } from 'vitest';
import {
	loginSchema,
	recoveryCodeSchema,
	registerSchema,
	resetPasswordSchema,
	totpCodeSchema,
} from './schemas';

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

	test('totpCodeSchema requires exactly six digits', () => {
		expect(totpCodeSchema.parse(' 123456 ')).toBe('123456');
		expect(totpCodeSchema.safeParse('12345').success).toBe(false);
		expect(totpCodeSchema.safeParse('1234567').success).toBe(false);
		expect(totpCodeSchema.safeParse('12a456').success).toBe(false);
	});

	test('recoveryCodeSchema normalizes formatting and rejects empty input', () => {
		expect(recoveryCodeSchema.parse(' abcd-efghj ')).toBe('ABCDEFGHJ');
		expect(recoveryCodeSchema.parse(' o1l0 2abc ')).toBe('01102ABC');
		expect(recoveryCodeSchema.safeParse('   ').success).toBe(false);
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
