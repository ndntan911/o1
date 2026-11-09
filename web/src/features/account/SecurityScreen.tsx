import { useCallback, useEffect, useId, useState, type FormEvent } from 'react';
import { toast } from 'sonner';
import { useQueryClient } from '@tanstack/react-query';
import { ErrorPanel } from '@/components/feedback/ErrorPanel';
import { PageSkeleton } from '@/components/feedback/Skeletons';
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
import { isApiProblem } from '@/lib/api/client';
import { problemFromError } from '@/lib/api/problem-from-error';
import type { ApiProblem } from '@/lib/api/problems';
import { totpCodeSchema } from '@/features/auth/schemas';
import type { TOTPDisableInput } from './api';
import { EnableTOTPDialog, RecoveryCodesPanel } from './EnableTOTPDialog';
import {
	evictTOTPMutation,
	useDisableTOTP,
	useRegenerateRecoveryCodes,
	useTOTPState,
} from './hooks';

function errorMessage(error: unknown): string {
	if (isApiProblem(error)) {
		return error.problem.detail ?? error.problem.title;
	}
	if (error instanceof Error) return error.message;
	return 'Please try again.';
}

export function SecurityScreen() {
	const stateQuery = useTOTPState();
	const [enableOpen, setEnableOpen] = useState(false);
	const [disableOpen, setDisableOpen] = useState(false);
	const [regenerateOpen, setRegenerateOpen] = useState(false);

	if (stateQuery.isPending) return <PageSkeleton />;

	if (!stateQuery.data) {
		return (
			<ErrorPanel
				title="Could not load security settings"
				message={errorMessage(stateQuery.error)}
				problem={problemFromError(stateQuery.error)}
				onRetry={() => void stateQuery.refetch()}
			/>
		);
	}

	const state = stateQuery.data;

	return (
		<section className="space-y-6">
			<div className="space-y-1">
				<h1 className="font-heading text-xl font-semibold">Security</h1>
				<p className="text-sm text-muted-foreground">
					Manage how you sign in to your account.
				</p>
			</div>

			<div className="space-y-4 rounded-xl border p-4">
				<h2 className="font-heading text-base font-medium">
					Two-factor authentication
				</h2>

				{state.enabled ? (
					<div className="space-y-4">
						<div className="space-y-1">
							<p className="text-sm font-medium">
								Two-factor authentication is on
							</p>
							<p className="text-sm text-muted-foreground">
								{state.recovery_codes_remaining}{' '}
								{state.recovery_codes_remaining === 1
									? 'recovery code'
									: 'recovery codes'}{' '}
								remaining
							</p>
						</div>
						<div className="flex flex-wrap gap-2">
							<Button
								type="button"
								variant="outline"
								onClick={() => setRegenerateOpen(true)}
							>
								Regenerate recovery codes
							</Button>
							<Button
								type="button"
								variant="outline"
								onClick={() => setDisableOpen(true)}
							>
								Disable two-factor authentication
							</Button>
						</div>
					</div>
				) : (
					<div className="space-y-4">
						<p className="max-w-2xl text-sm text-muted-foreground">
							Two-factor authentication adds a second step to sign-in using an
							authenticator app. You will receive one-time recovery codes when
							you enable it.
						</p>
						<Button type="button" onClick={() => setEnableOpen(true)}>
							Enable two-factor authentication
						</Button>
					</div>
				)}
			</div>

			{enableOpen ? (
				<EnableTOTPDialog onClose={() => setEnableOpen(false)} />
			) : null}
			{disableOpen ? (
				<DisableTOTPDialog onClose={() => setDisableOpen(false)} />
			) : null}
			{regenerateOpen ? (
				<RegenerateRecoveryCodesDialog
					onClose={() => setRegenerateOpen(false)}
				/>
			) : null}
		</section>
	);
}

interface FactorDialogProps {
	title: string;
	description: string;
	confirmLabel: string;
	pending: boolean;
	problem: ApiProblem | null;
	onClose: () => void;
	onSubmit: (input: TOTPDisableInput) => void;
}

function FactorDialog({
	title,
	description,
	confirmLabel,
	pending,
	problem,
	onClose,
	onSubmit,
}: FactorDialogProps) {
	const id = useId();
	const [password, setPassword] = useState('');
	const [method, setMethod] = useState<'totp' | 'recovery'>('totp');
	const [value, setValue] = useState('');

	const ready =
		password.length > 0 &&
		(method === 'totp'
			? totpCodeSchema.safeParse(value).success
			: value.trim().length > 0);

	function handleClose() {
		if (pending) return;
		onClose();
	}

	function handleSubmit(event: FormEvent<HTMLFormElement>) {
		event.preventDefault();
		if (!ready) return;
		onSubmit(
			method === 'totp'
				? { password, code: value.trim() }
				: { password, recovery_code: value.trim() },
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
					<DialogTitle>{title}</DialogTitle>
					<DialogDescription>{description}</DialogDescription>
				</DialogHeader>

				<form className="space-y-4" onSubmit={handleSubmit}>
					{problem ? <ProblemAlert problem={problem} /> : null}

					<div className="space-y-2">
						<Label htmlFor={`${id}-password`}>Password</Label>
						<Input
							id={`${id}-password`}
							type="password"
							autoComplete="current-password"
							value={password}
							onChange={(event) => setPassword(event.target.value)}
							required
						/>
					</div>

					<fieldset className="space-y-2">
						<legend className="text-sm font-medium">Second factor</legend>
						<label className="flex items-center gap-2 text-sm">
							<input
								type="radio"
								name={`${id}-factor`}
								checked={method === 'totp'}
								onChange={() => {
									setMethod('totp');
									setValue('');
								}}
							/>
							Use authenticator code
						</label>
						<label className="flex items-center gap-2 text-sm">
							<input
								type="radio"
								name={`${id}-factor`}
								checked={method === 'recovery'}
								onChange={() => {
									setMethod('recovery');
									setValue('');
								}}
							/>
							Use recovery code
						</label>
					</fieldset>

					<div className="space-y-2">
						<Label htmlFor={`${id}-value`}>
							{method === 'totp' ? 'Verification code' : 'Recovery code'}
						</Label>
						<Input
							id={`${id}-value`}
							type="text"
							inputMode={method === 'totp' ? 'numeric' : undefined}
							autoComplete="off"
							spellCheck={false}
							maxLength={method === 'totp' ? 6 : undefined}
							value={value}
							onChange={(event) =>
								setValue(
									method === 'totp'
										? event.target.value.replace(/\D/g, '')
										: event.target.value,
								)
							}
							required
						/>
					</div>

					<DialogFooter>
						<Button
							type="button"
							variant="outline"
							onClick={handleClose}
							disabled={pending}
						>
							Cancel
						</Button>
						<Button type="submit" disabled={!ready || pending}>
							{pending ? 'Working…' : confirmLabel}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

interface TOTPManagementDialogProps {
	onClose: () => void;
}

function DisableTOTPDialog({ onClose }: TOTPManagementDialogProps) {
	const queryClient = useQueryClient();
	const scope = useId();
	const disable = useDisableTOTP(scope);
	const resetDisable = disable.reset;
	const [problem, setProblem] = useState<ApiProblem | null>(null);

	const discard = useCallback(() => {
		resetDisable();
		evictTOTPMutation(queryClient, 'disable', scope);
	}, [queryClient, resetDisable, scope]);

	useEffect(() => () => discard(), [discard]);

	return (
		<FactorDialog
			title="Disable two-factor authentication"
			description="Confirm your password and one authentication factor to turn off two-factor authentication."
			confirmLabel="Disable two-factor authentication"
			pending={disable.isPending}
			problem={problem}
			onClose={onClose}
			onSubmit={(input) => {
				setProblem(null);
				disable.mutate(input, {
					onSuccess: () => {
						discard();
						toast.success('Two-factor authentication disabled');
						onClose();
					},
					onError: (error) => setProblem(problemFromError(error)),
				});
			}}
		/>
	);
}

function RegenerateRecoveryCodesDialog({ onClose }: TOTPManagementDialogProps) {
	const queryClient = useQueryClient();
	const scope = useId();
	const regenerate = useRegenerateRecoveryCodes(scope);
	const resetRegenerate = regenerate.reset;
	const [problem, setProblem] = useState<ApiProblem | null>(null);
	const [codes, setCodes] = useState<string[] | null>(null);

	const discard = useCallback(() => {
		resetRegenerate();
		evictTOTPMutation(queryClient, 'regenerate', scope);
	}, [queryClient, resetRegenerate, scope]);

	useEffect(() => () => discard(), [discard]);

	if (codes) {
		return (
			<Dialog open onOpenChange={(open) => !open && onClose()}>
				<DialogContent className="sm:max-w-md">
					<DialogHeader>
						<DialogTitle>Regenerate recovery codes</DialogTitle>
						<DialogDescription>
							Your previous recovery codes no longer work.
						</DialogDescription>
					</DialogHeader>
					<RecoveryCodesPanel codes={codes} onDone={onClose} />
				</DialogContent>
			</Dialog>
		);
	}

	return (
		<FactorDialog
			title="Regenerate recovery codes"
			description="Confirm your password and one authentication factor to replace your recovery codes."
			confirmLabel="Regenerate recovery codes"
			pending={regenerate.isPending}
			problem={problem}
			onClose={onClose}
			onSubmit={(input) => {
				setProblem(null);
				regenerate.mutate(input, {
					onSuccess: (result) => {
						setCodes(result.recovery_codes);
						discard();
					},
					onError: (error) => setProblem(problemFromError(error)),
				});
			}}
		/>
	);
}
