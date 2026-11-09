import { apiRequest } from '@/lib/api/client';
import type { LoginResponse } from '@/lib/api/types';

export type { AuthUser, LoginResponse } from '@/lib/api/types';

export interface RegisterInput {
	email: string;
	password: string;
}

export interface LoginInput {
	email: string;
	password: string;
}

export interface ResetPasswordInput {
	token: string;
	new_password: string;
}

export interface RegisterResponse {
	id: string;
	email: string;
}

export interface CompleteTOTPLoginInput {
	challenge_token: string;
	code?: string;
	recovery_code?: string;
}

export function register(input: RegisterInput): Promise<RegisterResponse> {
	return apiRequest<RegisterResponse>('/auth/register', {
		method: 'POST',
		body: { email: input.email, password: input.password },
	});
}

export function verifyEmail(token: string): Promise<void> {
	return apiRequest<void>('/auth/verify-email', {
		method: 'POST',
		body: { token },
	});
}

export function resendVerification(email: string): Promise<void> {
	return apiRequest<void>('/auth/verify-email/resend', {
		method: 'POST',
		body: { email },
	});
}

export function login(input: LoginInput): Promise<LoginResponse> {
	return apiRequest<LoginResponse>('/auth/login', {
		method: 'POST',
		body: { email: input.email, password: input.password },
	});
}

export function completeTOTPLogin(
	input: CompleteTOTPLoginInput,
): Promise<LoginResponse> {
	return apiRequest<LoginResponse>('/auth/login/totp', {
		method: 'POST',
		body: input,
	});
}

export function logout(): Promise<void> {
	return apiRequest<void>('/auth/logout', { method: 'POST' });
}

export function forgotPassword(email: string): Promise<void> {
	return apiRequest<void>('/auth/password/forgot', {
		method: 'POST',
		body: { email },
	});
}

export function resetPassword(input: ResetPasswordInput): Promise<void> {
	return apiRequest<void>('/auth/password/reset', {
		method: 'POST',
		body: { token: input.token, new_password: input.new_password },
	});
}
