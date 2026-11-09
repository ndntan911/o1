import { useCallback, useEffect, useId, useState, type FormEvent } from 'react';
import { toast } from 'sonner';
import { useQueryClient } from '@tanstack/react-query';
import { QRCodeSVG } from 'qrcode.react';
import { ProblemAlert } from '@/components/feedback/ProblemAlert';
import { Button } from '@/components/ui/button';
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { problemFromError } from '@/lib/api/problem-from-error';
import type { ApiProblem } from '@/lib/api/problems';
import type { TOTPSetup } from './api';
import { evictTOTPMutation, useEnableTOTP, useSetupTOTP } from './hooks';

export interface EnableTOTPDialogProps {
	onClose: () => void;
}

export interface RecoveryCodesPanelProps {
	codes: string[];
	onDone: () => void;
}

export function RecoveryCodesPanel({ codes, onDone }: RecoveryCodesPanelProps) {
	async function handleCopy() {
		try {
			await navigator.clipboard.writeText(codes.join('\n'));
			toast.success('Recovery codes copied');
		} catch {
			toast.error('Could not copy recovery codes');
		}
	}

	return (
		<div className="space-y-4">
			<div className="space-y-2">
				<Label>Recovery codes</Label>
				<ul
					className="grid max-h-64 gap-1 overflow-y-auto rounded-lg border bg-muted/40 p-3 font-mono text-sm"
					aria-label="Recovery codes"
				>
					{codes.map((code) => (
						<li key={code}>{code}</li>
					))}
				</ul>
				<p
					role="status"
					aria-live="polite"
					className="text-sm text-destructive"
				>
					Store these codes somewhere safe. Each code can be used once and will
					never be shown again.
				</p>
			</div>
			<DialogFooter>
				<Button
					type="button"
					variant="outline"
					onClick={() => void handleCopy()}
				>
					Copy codes
				</Button>
				<Button type="button" onClick={onDone}>
					Done
				</Button>
			</DialogFooter>
		</div>
	);
}

export function EnableTOTPDialog({ onClose }: EnableTOTPDialogProps) {
	const queryClient = useQueryClient();
	const scope = useId();
	const setupMutation = useSetupTOTP(scope);
	const enableMutation = useEnableTOTP(scope);
	const resetSetup = setupMutation.reset;
	const resetEnable = enableMutation.reset;

	const [step, setStep] = useState<'password' | 'verify' | 'codes'>('password');
	const [password, setPassword] = useState('');
	const [verifyPassword, setVerifyPassword] = useState('');
	const [code, setCode] = useState('');
	const [setup, setSetup] = useState<TOTPSetup | null>(null);
	const [codes, setCodes] = useState<string[] | null>(null);
	const [problem, setProblem] = useState<ApiProblem | null>(null);

	const discardSetup = useCallback(() => {
		// reset() detaches the observer but the mutation cache entry still holds
		// the one-time secret, so remove it explicitly.
		resetSetup();
		evictTOTPMutation(queryClient, 'setup', scope);
	}, [queryClient, resetSetup, scope]);

	const discardEnable = useCallback(() => {
		resetEnable();
		evictTOTPMutation(queryClient, 'enable', scope);
	}, [queryClient, resetEnable, scope]);

	useEffect(
		() => () => {
			discardSetup();
			discardEnable();
		},
		[discardEnable, discardSetup],
	);

	const pending = setupMutation.isPending || enableMutation.isPending;

	function handleClose() {
		if (pending) return;
		setSetup(null);
		setCodes(null);
		setPassword('');
		setVerifyPassword('');
		setCode('');
		setProblem(null);
		setStep('password');
		discardSetup();
		discardEnable();
		onClose();
	}

	function handleStart(event: FormEvent<HTMLFormElement>) {
		event.preventDefault();
		setProblem(null);
		setupMutation.mutate(
			{ password },
			{
				onSuccess: (result) => {
					setSetup(result);
					setStep('verify');
					discardSetup();
				},
				onError: (error) => setProblem(problemFromError(error)),
			},
		);
	}

	function handleEnable(event: FormEvent<HTMLFormElement>) {
		event.preventDefault();
		setProblem(null);
		enableMutation.mutate(
			{ password: verifyPassword, code },
			{
				onSuccess: (result) => {
					setCodes(result.recovery_codes);
					setSetup(null);
					setPassword('');
					setVerifyPassword('');
					setCode('');
					setStep('codes');
					discardEnable();
				},
				onError: (error) => setProblem(problemFromError(error)),
			},
		);
	}

	return (
		<Dialog open onOpenChange={(open) => !open && handleClose()}>
			<DialogContent
				className="sm:max-w-md"
				onEscapeKeyDown={(event) => {
					if (pending) event.preventDefault();
				}}
				onPointerDownOutside={(event) => {
					if (pending) event.preventDefault();
				}}
			>
				<DialogHeader>
					<DialogTitle>Enable two-factor authentication</DialogTitle>
					<DialogDescription>
						{step === 'password'
							? 'Confirm your password to start setting up an authenticator app.'
							: step === 'verify'
								? 'Scan the QR code, then enter the current code from your authenticator app.'
								: 'Save your recovery codes before finishing.'}
					</DialogDescription>
				</DialogHeader>

				{step === 'password' ? (
					<form className="space-y-4" onSubmit={handleStart}>
						{problem ? <ProblemAlert problem={problem} /> : null}

						<div className="space-y-2">
							<Label htmlFor="totp-setup-password">Password</Label>
							<Input
								id="totp-setup-password"
								type="password"
								autoComplete="current-password"
								value={password}
								onChange={(event) => {
									setPassword(event.target.value);
									setProblem(null);
								}}
								required
							/>
						</div>

						<DialogFooter>
							<Button
								type="button"
								variant="outline"
								onClick={handleClose}
								disabled={setupMutation.isPending}
							>
								Cancel
							</Button>
							<Button
								type="submit"
								disabled={!password || setupMutation.isPending}
							>
								{setupMutation.isPending ? 'Checking…' : 'Continue'}
							</Button>
						</DialogFooter>
					</form>
				) : null}

				{step === 'verify' && setup ? (
					<form className="space-y-4" onSubmit={handleEnable}>
						<div className="flex justify-center rounded-lg border bg-white p-3">
							<div data-testid="totp-qr">
								<QRCodeSVG
									value={setup.otpauth_uri}
									size={176}
									title="Two-factor authentication QR code"
								/>
							</div>
						</div>

						<div className="space-y-1">
							<Label>Secret</Label>
							<code className="block break-all rounded-lg border bg-muted/40 p-2 font-mono text-xs">
								{setup.secret}
							</code>
						</div>

						{problem ? <ProblemAlert problem={problem} /> : null}

						<div className="space-y-2">
							<Label htmlFor="totp-enable-password">Password</Label>
							<Input
								id="totp-enable-password"
								type="password"
								autoComplete="current-password"
								value={verifyPassword}
								onChange={(event) => {
									setVerifyPassword(event.target.value);
									setProblem(null);
								}}
								required
							/>
						</div>

						<div className="space-y-2">
							<Label htmlFor="totp-enable-code">Verification code</Label>
							<Input
								id="totp-enable-code"
								type="text"
								inputMode="numeric"
								autoComplete="one-time-code"
								maxLength={6}
								value={code}
								onChange={(event) => {
									setCode(event.target.value.replace(/\D/g, ''));
									setProblem(null);
								}}
								required
							/>
						</div>

						<DialogFooter>
							<Button
								type="button"
								variant="outline"
								onClick={handleClose}
								disabled={enableMutation.isPending}
							>
								Cancel
							</Button>
							<Button
								type="submit"
								disabled={!verifyPassword || code.length === 0 || pending}
							>
								{enableMutation.isPending ? 'Enabling…' : 'Verify and enable'}
							</Button>
						</DialogFooter>
					</form>
				) : null}

				{step === 'codes' && codes ? (
					<RecoveryCodesPanel codes={codes} onDone={handleClose} />
				) : null}
			</DialogContent>
		</Dialog>
	);
}
