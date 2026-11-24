<script lang="ts">
	import { api, type Me } from '#lib/api.ts';

	type Props = { me: Me; onchange: () => void };
	let { me, onchange }: Props = $props();

	let invite = $state<{ url: string; expiresAt: string } | null>(null);
	let busy = $state(false);
	let error = $state('');
	let copied = $state(false);

	async function makeInvite() {
		busy = true;
		error = '';
		try {
			invite = await api.createInvite();
			copied = false;
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}

	async function copy() {
		if (!invite) return;
		try {
			await navigator.clipboard.writeText(invite.url);
			copied = true;
		} catch {
			/* select the text instead */
		}
	}

	async function rename() {
		const label = prompt('Name for this Yoto account', me.yoto.label)?.trim();
		if (!label || label === me.yoto.label) return;
		await api.renameYoto(label);
		onchange();
	}

	async function leave() {
		const others = (me.yoto.members ?? []).filter((m) => m !== me.user);
		const msg = others.length
			? `Leave “${me.yoto.label}”? ${others.join(', ')} keep${others.length === 1 ? 's' : ''} it and its cards.`
			: `Disconnect “${me.yoto.label}”? Its cards keep playing; connect the same Yoto account again to get them back.`;
		if (!confirm(msg)) return;
		await api.leaveYoto();
		onchange();
	}
</script>

<section class="account">
	<div class="head">
		<div>
			<p class="eyebrow">Yoto account</p>
			<h2>
				{me.yoto.label}
				<button class="link" onclick={rename} aria-label="Rename Yoto account">Rename</button>
			</h2>
			<p class="muted members">
				Shared with:
				{#each me.yoto.members ?? [] as m (m)}
					<span class="chip" class:you={m === me.user}>{m === me.user ? `${m} (you)` : m}</span>
				{/each}
			</p>
		</div>
		<div class="actions">
			<button class="btn small" onclick={makeInvite} disabled={busy}>Invite someone</button>
			<button class="btn small" onclick={leave}>Leave</button>
		</div>
	</div>
	{#if invite}
		<div class="invite">
			<p class="muted">
				Send this link to someone with a login here. It works once and expires
				{new Date(invite.expiresAt).toLocaleDateString()}.
			</p>
			<div class="row">
				<input type="text" readonly value={invite.url} onfocus={(e) => e.currentTarget.select()} />
				<button class="btn small primary" onclick={copy}>{copied ? 'Copied' : 'Copy'}</button>
			</div>
		</div>
	{/if}
	{#if error}<p class="error">{error}</p>{/if}
</section>

<style>
	.account {
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: 0.9rem 1rem;
		margin-bottom: 1rem;
	}
	.head {
		display: flex;
		justify-content: space-between;
		gap: 1rem;
		align-items: flex-start;
		flex-wrap: wrap;
	}
	.eyebrow {
		margin: 0;
		font-size: 0.75rem;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		color: var(--muted);
		font-weight: 700;
	}
	h2 {
		font-size: 1.1rem;
		margin: 0.1rem 0 0.35rem;
		display: flex;
		align-items: baseline;
		gap: 0.5rem;
	}
	.link {
		border: 0;
		background: none;
		color: var(--accent);
		font-size: 0.8rem;
		cursor: pointer;
		padding: 0;
	}
	.members {
		margin: 0;
		font-size: 0.875rem;
		display: flex;
		flex-wrap: wrap;
		gap: 0.35rem;
		align-items: center;
	}
	.chip {
		padding: 0.1em 0.6em;
		border-radius: 999px;
		background: var(--surface-2);
		color: var(--text);
	}
	.chip.you {
		background: var(--accent-soft);
	}
	.actions {
		display: flex;
		gap: 0.4rem;
	}
	.invite {
		margin-top: 0.75rem;
		padding-top: 0.75rem;
		border-top: 1px solid var(--border);
	}
	.invite p {
		margin: 0 0 0.4rem;
		font-size: 0.85rem;
	}
	.row {
		display: flex;
		gap: 0.4rem;
	}
	.error {
		margin: 0.5rem 0 0;
		color: var(--danger);
		font-size: 0.85rem;
	}
</style>
