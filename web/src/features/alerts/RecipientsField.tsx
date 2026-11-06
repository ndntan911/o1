import {
	forwardRef,
	useId,
	useImperativeHandle,
	useState,
	type KeyboardEvent,
} from 'react';
import { XIcon } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';

const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const MAX_RECIPIENTS = 20;

export interface RecipientsFieldHandle {
	/**
	 * Add the current draft to the list when it is valid. Returns the committed
	 * list, or null when the draft is invalid and an inline error is shown.
	 */
	commitDraft: () => string[] | null;
}

export interface RecipientsFieldProps {
	recipients: string[];
	onChange: (recipients: string[]) => void;
	disabled?: boolean;
	label?: string;
	maxRecipients?: number;
}

export const RecipientsField = forwardRef<
	RecipientsFieldHandle,
	RecipientsFieldProps
>(function RecipientsField(
	{
		recipients,
		onChange,
		disabled = false,
		label = 'Recipients',
		maxRecipients = MAX_RECIPIENTS,
	},
	ref,
) {
	const inputId = useId();
	const errorId = useId();
	const [draft, setDraft] = useState('');
	const [error, setError] = useState<string | null>(null);

	function commitDraft(): string[] | null {
		const address = draft.trim().toLowerCase();
		if (!address) return recipients;
		if (!EMAIL_PATTERN.test(address)) {
			setError('Enter a valid email address');
			return null;
		}
		if (recipients.includes(address)) {
			setError('This recipient is already added');
			return null;
		}
		if (recipients.length >= maxRecipients) {
			setError(`You can add up to ${maxRecipients} recipients`);
			return null;
		}

		const next = [...recipients, address];
		onChange(next);
		setDraft('');
		setError(null);
		return next;
	}

	useImperativeHandle(ref, () => ({ commitDraft }));

	function addRecipient() {
		if (!draft.trim()) {
			setError('Enter an email address');
			return;
		}
		commitDraft();
	}

	function removeRecipient(address: string) {
		onChange(recipients.filter((recipient) => recipient !== address));
		setError(null);
	}

	function handleKeyDown(event: KeyboardEvent<HTMLInputElement>) {
		if (event.key !== 'Enter') return;
		event.preventDefault();
		addRecipient();
	}

	return (
		<div className="space-y-3">
			<div className="space-y-1">
				<h3 className="text-sm font-medium">{label}</h3>
				<p className="text-sm text-muted-foreground">
					Incident emails are sent to every address in this list.
				</p>
			</div>

			{recipients.length > 0 ? (
				<ul aria-label={label} className="flex flex-wrap gap-2">
					{recipients.map((recipient) => (
						<li
							key={recipient}
							className="flex items-center gap-1 rounded-lg border py-1 pr-1 pl-2.5 text-sm"
						>
							<span>{recipient}</span>
							<Button
								type="button"
								variant="ghost"
								size="icon"
								aria-label={`Remove ${recipient}`}
								disabled={disabled}
								onClick={() => removeRecipient(recipient)}
							>
								<XIcon aria-hidden />
							</Button>
						</li>
					))}
				</ul>
			) : (
				<p className="text-sm text-muted-foreground">No recipients</p>
			)}

			<div className="space-y-2">
				<Label htmlFor={inputId}>Add recipient</Label>
				<div className="flex flex-wrap items-start gap-2">
					<Input
						id={inputId}
						type="email"
						autoComplete="off"
						placeholder="ops@example.com"
						value={draft}
						disabled={disabled}
						aria-invalid={Boolean(error)}
						aria-describedby={error ? errorId : undefined}
						onChange={(event) => {
							setDraft(event.target.value);
							setError(null);
						}}
						onKeyDown={handleKeyDown}
					/>
					<Button
						type="button"
						variant="outline"
						disabled={disabled || recipients.length >= maxRecipients}
						onClick={addRecipient}
					>
						Add
					</Button>
				</div>
				{error ? (
					<p id={errorId} role="alert" className="text-sm text-destructive">
						{error}
					</p>
				) : null}
			</div>
		</div>
	);
});
