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
