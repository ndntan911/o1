import { z } from 'zod';

export const emailSchema = z
	.string()
	.trim()
	.toLowerCase()
	.email('Enter a valid email address');

export const passwordSchema = z
	.string()
	.min(12, 'Password must be at least 12 characters')
	.max(128, 'Password must be at most 128 characters');

export const loginSchema = z.object({
	email: emailSchema,
	password: z.string().min(1, 'Password is required'),
});

export const totpCodeSchema = z
	.string()
	.trim()
	.regex(/^\d{6}$/, 'Enter a 6-digit authentication code');

// Mirror the server's normalization so formatting differences (case, spaces,
// the display separator, and ambiguous letters) still match a recovery code.
function normalizeRecoveryCode(value: string): string {
	return value
		.trim()
		.toUpperCase()
		.replace(/[-\s]+/g, '')
		.replace(/O/g, '0')
		.replace(/I/g, '1')
		.replace(/L/g, '1');
}

export const recoveryCodeSchema = z
	.string()
	.transform(normalizeRecoveryCode)
	.refine((value) => value.length > 0, 'Enter a recovery code');

export const registerSchema = z
	.object({
		email: emailSchema,
		password: passwordSchema,
		confirmPassword: z.string(),
	})
	.refine((v) => v.password === v.confirmPassword, {
		path: ['confirmPassword'],
		message: 'Passwords do not match',
	});

export const forgotPasswordSchema = z.object({ email: emailSchema });

export const resetPasswordSchema = z
	.object({ password: passwordSchema, confirmPassword: z.string() })
	.refine((v) => v.password === v.confirmPassword, {
		path: ['confirmPassword'],
		message: 'Passwords do not match',
	});

export const verifyEmailSchema = z.object({ token: z.string().min(1) });

export type LoginValues = z.infer<typeof loginSchema>;
export type RegisterValues = z.infer<typeof registerSchema>;
export type ForgotPasswordValues = z.infer<typeof forgotPasswordSchema>;
export type ResetPasswordValues = z.infer<typeof resetPasswordSchema>;
export type VerifyEmailValues = z.infer<typeof verifyEmailSchema>;
