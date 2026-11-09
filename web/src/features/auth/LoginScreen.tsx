import { useState, type FormEvent } from 'react';
import { Link, useNavigate } from '@tanstack/react-router';
import { useQueryClient } from '@tanstack/react-query';
import { zodResolver } from '@hookform/resolvers/zod';
import { useForm } from 'react-hook-form';
import { ProblemAlert } from '@/components/feedback/ProblemAlert';
import { Button } from '@/components/ui/button';
import { CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { isApiProblem } from '@/lib/api/client';
import type { ApiProblem } from '@/lib/api/problems';
import { applyProblemToForm } from '@/lib/forms';
import { queryKeys } from '@/lib/query/keys';
import { clearWorkspaceSuspended } from '@/lib/workspace-status';
import { completeTOTPLogin, login, resendVerification } from './api';
import { safeRedirect } from './guards';
import {
	loginSchema,
	recoveryCodeSchema,
	totpCodeSchema,
	type LoginValues,
} from './schemas';

export interface LoginScreenProps {
	redirectTo?: string;
}

type LoginStep = 'password' | 'totp' | 'recovery';

function unexpectedProblem(): ApiProblem {
	return {
		type: 'about:blank',
		title: 'Something went wrong',
		status: 0,
		code: 'internal_error',
		detail: 'Please try again.',
	};
}

export function LoginScreen({ redirectTo }: LoginScreenProps) {
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const [step, setStep] = useState<LoginStep>('password');
	const [challengeToken, setChallengeToken] = useState<string | null>(null);
	const [code, setCode] = useState('');
	const [codeError, setCodeError] = useState<string | null>(null);
	const [verifying, setVerifying] = useState(false);
	const [problem, setProblem] = useState<ApiProblem | null>(null);
	const [resendState, setResendState] = useState<
		'idle' | 'sending' | 'sent' | 'error'
	>('idle');
	const form = useForm<LoginValues>({
		resolver: zodResolver(loginSchema),
		defaultValues: { email: '', password: '' },
	});

	async function finishLogin() {
		// A session-expiry logout only evicts `me`, so remove every other
		// cached query before showing the new session. Otherwise a fresh
		// non-`me` request can briefly display user A's data to user B in
		// the same tab.
		queryClient.removeQueries({
			predicate: (query) => query.queryKey[0] !== 'me',
		});
		clearWorkspaceSuspended();
		await queryClient.invalidateQueries({ queryKey: queryKeys.me });
		await navigate({ to: safeRedirect(redirectTo) });
	}

	const onSubmit = form.handleSubmit(async (values) => {
		setProblem(null);
		setResendState('idle');
		try {
			const response = await login(values);
			if (response.mfa_required) {
				if (!response.challenge_token) {
					setProblem(unexpectedProblem());
					return;
				}
				// A second factor is required: keep the challenge in component
				// state only and switch steps without touching caches or
				// navigating until the factor succeeds.
				setChallengeToken(response.challenge_token);
				setStep('totp');
				setCode('');
				setCodeError(null);
				return;
			}
			await finishLogin();
		} catch (error) {
			if (!isApiProblem(error)) {
				setProblem(unexpectedProblem());
				return;
			}
			const unmapped = applyProblemToForm(error.problem, form, {
				email: 'email',
				password: 'password',
			});
			const hasFields = Object.keys(error.problem.fields ?? {}).length > 0;
			setProblem(!hasFields || unmapped.length > 0 ? error.problem : null);
		}
	});

	async function onSecondFactorSubmit(event: FormEvent<HTMLFormElement>) {
		event.preventDefault();
		if (!challengeToken) {
			backToPassword();
			return;
		}
		setProblem(null);
		setCodeError(null);
		const schema = step === 'recovery' ? recoveryCodeSchema : totpCodeSchema;
		const parsed = schema.safeParse(code);
		if (!parsed.success) {
			setCodeError(parsed.error.issues[0]?.message ?? 'Enter a valid code');
			return;
		}

		setVerifying(true);
		try {
			await completeTOTPLogin(
				step === 'recovery'
					? { challenge_token: challengeToken, recovery_code: parsed.data }
					: { challenge_token: challengeToken, code: parsed.data },
			);
			await finishLogin();
		} catch (error) {
			setProblem(isApiProblem(error) ? error.problem : unexpectedProblem());
		} finally {
			setVerifying(false);
		}
	}

	function switchStep(next: LoginStep) {
		setStep(next);
		setCode('');
		setCodeError(null);
		setProblem(null);
	}

	function backToPassword() {
		setChallengeToken(null);
		switchStep('password');
	}

	async function handleResend() {
		const email = form.getValues('email');
		setResendState('sending');
		try {
			await resendVerification(email);
			setResendState('sent');
		} catch {
			setResendState('error');
		}
	}

	const emailError = form.formState.errors.email;
	const passwordError = form.formState.errors.password;
	const isSubmitting = form.formState.isSubmitting;

	return (
		<>
			<CardHeader>
				<CardTitle>Sign in</CardTitle>
				<CardDescription>
					{step === 'password'
						? 'Sign in to manage your uptime monitors.'
						: 'Enter the second factor for your account.'}
				</CardDescription>
			</CardHeader>
			{step === 'password' ? (
				<form className="space-y-4 px-4" onSubmit={onSubmit} noValidate>
					<div className="space-y-1.5">
						<Label htmlFor="login-email">Email</Label>
						<Input
							id="login-email"
							type="email"
							autoComplete="email"
							aria-invalid={emailError ? true : undefined}
							aria-describedby={emailError ? 'login-email-error' : undefined}
							{...form.register('email')}
						/>
						{emailError ? (
							<p id="login-email-error" className="text-sm text-destructive">
								{emailError.message}
							</p>
						) : null}
					</div>

					<div className="space-y-1.5">
						<Label htmlFor="login-password">Password</Label>
						<Input
							id="login-password"
							type="password"
							autoComplete="current-password"
							aria-invalid={passwordError ? true : undefined}
							aria-describedby={
								passwordError ? 'login-password-error' : undefined
							}
							{...form.register('password')}
						/>
						{passwordError ? (
							<p id="login-password-error" className="text-sm text-destructive">
								{passwordError.message}
							</p>
						) : null}
					</div>

					<ProblemAlert problem={problem} />

					{problem?.code === 'email_not_verified' ? (
						<div className="space-y-2 rounded-lg border p-3">
							<p className="text-sm text-muted-foreground">
								Verify your email before signing in. We can send another
								verification link.
							</p>
							<Button
								type="button"
								variant="outline"
								onClick={() => void handleResend()}
								disabled={resendState === 'sending'}
							>
								{resendState === 'sending'
									? 'Sending…'
									: 'Resend verification email'}
							</Button>
							{resendState === 'sent' ? (
								<p className="text-sm text-muted-foreground" role="status">
									Verification email sent.
								</p>
							) : null}
							{resendState === 'error' ? (
								<p className="text-sm text-destructive" role="status">
									Could not send the verification email. Try again.
								</p>
							) : null}
						</div>
					) : null}

					<Button type="submit" className="w-full" disabled={isSubmitting}>
						{isSubmitting ? 'Signing in…' : 'Sign in'}
					</Button>

					<div className="flex flex-col gap-2 text-sm text-muted-foreground">
						<Link to="/register" className="underline underline-offset-4">
							Create an account
						</Link>
						<Link
							to="/forgot-password"
							className="underline underline-offset-4"
						>
							Forgot your password?
						</Link>
					</div>
				</form>
			) : (
				<form
					className="space-y-4 px-4"
					onSubmit={onSecondFactorSubmit}
					noValidate
				>
					<div className="space-y-1.5">
						<Label htmlFor="login-second-factor">
							{step === 'recovery' ? 'Recovery code' : 'Authentication code'}
						</Label>
						{step === 'recovery' ? (
							<Input
								id="login-second-factor"
								type="text"
								autoComplete="off"
								autoCapitalize="characters"
								spellCheck={false}
								aria-invalid={codeError ? true : undefined}
								aria-describedby={
									codeError ? 'login-second-factor-error' : undefined
								}
								value={code}
								onChange={(event) => setCode(event.target.value)}
							/>
						) : (
							<Input
								id="login-second-factor"
								type="text"
								inputMode="numeric"
								autoComplete="one-time-code"
								maxLength={6}
								aria-invalid={codeError ? true : undefined}
								aria-describedby={
									codeError ? 'login-second-factor-error' : undefined
								}
								value={code}
								onChange={(event) =>
									setCode(event.target.value.replace(/\D/g, ''))
								}
							/>
						)}
						{codeError ? (
							<p
								id="login-second-factor-error"
								className="text-sm text-destructive"
							>
								{codeError}
							</p>
						) : null}
					</div>

					<ProblemAlert problem={problem} />

					<Button type="submit" className="w-full" disabled={verifying}>
						{verifying ? 'Verifying…' : 'Verify'}
					</Button>

					<div className="flex flex-col gap-2 text-sm text-muted-foreground">
						{step === 'recovery' ? (
							<button
								type="button"
								className="self-start underline underline-offset-4"
								onClick={() => switchStep('totp')}
							>
								Use an authenticator code
							</button>
						) : (
							<button
								type="button"
								className="self-start underline underline-offset-4"
								onClick={() => switchStep('recovery')}
							>
								Use a recovery code
							</button>
						)}
						<button
							type="button"
							className="self-start underline underline-offset-4"
							onClick={backToPassword}
						>
							Back to sign in
						</button>
					</div>
				</form>
			)}
		</>
	);
}
