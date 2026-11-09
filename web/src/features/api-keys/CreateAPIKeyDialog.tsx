import { useState, type FormEvent } from 'react';
import { toast } from 'sonner';
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
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from '@/components/ui/select';
import { problemFromError } from '@/lib/api/problem-from-error';
import type { ApiProblem } from '@/lib/api/problems';
import type { APIKeyRole, CreateAPIKeyInput } from '@/lib/api/types';
import { useCreateAPIKey } from './hooks';

export interface CreateAPIKeyDialogProps {
	workspaceId: string;
	onClose: () => void;
}

const ROLES: Array<{ value: APIKeyRole; label: string }> = [
	{ value: 'viewer', label: 'Viewer' },
	{ value: 'member', label: 'Member' },
];

export function CreateAPIKeyDialog({
	workspaceId,
	onClose,
}: CreateAPIKeyDialogProps) {
	const create = useCreateAPIKey(workspaceId);
	const [name, setName] = useState('');
	const [role, setRole] = useState<APIKeyRole>('viewer');
	const [secret, setSecret] = useState<string | null>(null);
	const [problem, setProblem] = useState<ApiProblem | null>(null);

	function handleClose() {
		// The secret exists only in this component's state. Clearing it before
		// unmounting guarantees a reopened dialog can never show it again.
		setSecret(null);
		setName('');
		setRole('viewer');
		setProblem(null);
		create.reset();
		onClose();
	}

	function handleSubmit(event: FormEvent<HTMLFormElement>) {
		event.preventDefault();
		setProblem(null);

		const input: CreateAPIKeyInput = { name: name.trim(), role };
		create.mutate(input, {
			onSuccess: (created) => {
				setSecret(created.secret);
				// Drop the mutation result from React Query's mutation cache so the
				// secret only ever lives in this component's state.
				create.reset();
			},
			onError: (error) => setProblem(problemFromError(error)),
		});
	}

	async function handleCopy() {
		if (!secret) return;
		try {
			await navigator.clipboard.writeText(secret);
			toast.success('API key copied');
		} catch {
			toast.error('Could not copy the key');
		}
	}

	return (
		<Dialog open onOpenChange={(open) => !open && handleClose()}>
			<DialogContent className="sm:max-w-md">
				<DialogHeader>
					<DialogTitle>Create API key</DialogTitle>
					<DialogDescription>
						Create a key to authenticate scripts against this workspace.
					</DialogDescription>
				</DialogHeader>

				{secret ? (
					<div className="space-y-4">
						<div className="space-y-2">
							<Label htmlFor="api-key-secret">API key</Label>
							<Input
								id="api-key-secret"
								readOnly
								value={secret}
								className="font-mono"
								onFocus={(event) => event.currentTarget.select()}
							/>
							<p className="text-sm text-destructive">
								Copy this key now. It will never be shown again.
							</p>
						</div>
						<DialogFooter>
							<Button
								type="button"
								variant="outline"
								onClick={() => void handleCopy()}
							>
								Copy key
							</Button>
							<Button type="button" onClick={handleClose}>
								Done
							</Button>
						</DialogFooter>
					</div>
				) : (
					<form className="space-y-4" onSubmit={handleSubmit}>
						{problem ? <ProblemAlert problem={problem} /> : null}

						<div className="space-y-2">
							<Label htmlFor="api-key-name">Name</Label>
							<Input
								id="api-key-name"
								value={name}
								maxLength={60}
								onChange={(event) => {
									setName(event.target.value);
									setProblem(null);
								}}
								required
							/>
						</div>

						<div className="space-y-2">
							<Label htmlFor="api-key-role">Role</Label>
							<Select
								value={role}
								onValueChange={(value) => setRole(value as APIKeyRole)}
							>
								<SelectTrigger
									id="api-key-role"
									aria-label="Role"
									className="w-full"
								>
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									{ROLES.map((option) => (
										<SelectItem key={option.value} value={option.value}>
											{option.label}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>

						<DialogFooter>
							<Button
								type="button"
								variant="outline"
								onClick={handleClose}
								disabled={create.isPending}
							>
								Cancel
							</Button>
							<Button type="submit" disabled={create.isPending}>
								{create.isPending ? 'Creating…' : 'Create key'}
							</Button>
						</DialogFooter>
					</form>
				)}
			</DialogContent>
		</Dialog>
	);
}
