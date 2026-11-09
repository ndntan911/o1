import { apiRequest } from '@/lib/api/client';

export interface TOTPState {
	enabled: boolean;
	recovery_codes_remaining: number;
}

export interface TOTPSetup {
	secret: string;
	otpauth_uri: string;
}

export interface TOTPDisableInput {
	password: string;
	code?: string;
	recovery_code?: string;
}

export interface TOTPRegenerateInput {
	password: string;
	code?: string;
	recovery_code?: string;
}

export interface TOTPEnableInput {
	password: string;
	code: string;
}

export interface RecoveryCodesResponse {
	recovery_codes: string[];
}

const totpPath = '/auth/2fa';

export function getTOTPState(): Promise<TOTPState> {
	return apiRequest<TOTPState>(totpPath);
}

export function setupTOTP(input: { password: string }): Promise<TOTPSetup> {
	return apiRequest<TOTPSetup>(`${totpPath}/setup`, {
		method: 'POST',
		body: { password: input.password },
	});
}

export function enableTOTP(
	input: TOTPEnableInput,
): Promise<RecoveryCodesResponse> {
	return apiRequest<RecoveryCodesResponse>(`${totpPath}/enable`, {
		method: 'POST',
		body: { password: input.password, code: input.code },
	});
}

export function disableTOTP(input: TOTPDisableInput): Promise<void> {
	return apiRequest<void>(`${totpPath}/disable`, {
		method: 'POST',
		body: input,
	});
}

export function regenerateRecoveryCodes(
	input: TOTPRegenerateInput,
): Promise<RecoveryCodesResponse> {
	return apiRequest<RecoveryCodesResponse>(`${totpPath}/recovery-codes`, {
		method: 'POST',
		body: input,
	});
}
