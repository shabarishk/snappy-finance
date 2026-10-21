import { useCallback, useEffect, useMemo, useState } from "react";
import { ArrowDownLeft, ArrowUpRight, Banknote, Landmark, LockKeyhole, LogOut, RefreshCw, Wallet } from "lucide-react";

type Profile = {
  id: string;
  username: string;
  role: "admin" | "user";
};

const dashboardCards = [
  { id: "balance", label: "Total account balance" },
  { id: "income", label: "Income this month" },
  { id: "spending", label: "Spending this month" },
  { id: "net", label: "Monthly net" },
];

type Account = {
  account_id: string;
  name: string;
  official_name: string | null;
  mask: string | null;
  type: string;
  subtype: string;
  current_balance: number | null;
  iso_currency_code: string | null;
  demo_data: boolean;
};

type Transaction = {
  transaction_id: string;
  account_id: string;
  name: string;
  merchant_name: string | null;
  amount: number;
  date: string;
  pending: boolean;
  category_primary: string | null;
  category_detailed: string | null;
  iso_currency_code: string | null;
};

declare global {
  interface Window {
    Plaid?: {
      create: (options: {
        token: string;
        onSuccess: (publicToken: string) => void;
        onExit: (error: unknown) => void;
      }) => { open: () => void };
    };
  }
}

async function api<T>(path: string, options?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", ...options?.headers },
  });
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as { error?: string };
    throw new Error(body.error ?? `Request failed (${response.status})`);
  }
  return response.json() as Promise<T>;
}

function money(value: number | null, currency: string | null = "USD") {
  if (value === null) return "—";
  return new Intl.NumberFormat(undefined, {
    style: "currency",
    currency: currency || "USD",
  }).format(value);
}

export default function App() {
  const [authenticated, setAuthenticated] = useState<boolean | null>(null);
  const [profile, setProfile] = useState<Profile | null>(null);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [hiddenCards, setHiddenCards] = useState<string[]>([]);
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [newUsername, setNewUsername] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [currentPassword, setCurrentPassword] = useState("");
  const [replacementPassword, setReplacementPassword] = useState("");
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [transactions, setTransactions] = useState<Transaction[]>([]);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  const refresh = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const [accountRows, transactionRows] = await Promise.all([
        api<Account[]>("/api/accounts"),
        api<Transaction[]>("/api/transactions"),
      ]);
      setAccounts(accountRows);
      setTransactions(transactionRows);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not load financial data");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    api<{ authenticated: boolean; user: Profile | null }>("/api/auth/session")
      .then(async ({ authenticated: value, user }) => {
        setAuthenticated(value);
        setProfile(user);
        if (user) {
          const preferences = await api<{ hidden_cards: string[] }>("/api/me/preferences");
          setHiddenCards(preferences.hidden_cards);
          if (user.role === "admin") {
            setProfiles(await api<Profile[]>("/api/admin/users"));
          }
          await refresh();
        }
      })
      .catch(() => {
        setAuthenticated(false);
        setProfile(null);
      });
  }, [refresh]);

  const totals = useMemo(() => {
    const today = new Date();
    const month = today.toISOString().slice(0, 7);
    const current = transactions.filter((item) => item.date.startsWith(month));
    const spent = current.filter((item) => item.amount > 0).reduce((sum, item) => sum + item.amount, 0);
    const income = current.filter((item) => item.amount < 0).reduce((sum, item) => sum + Math.abs(item.amount), 0);
    return { spent, income, net: income - spent };
  }, [transactions]);

  async function signIn(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    try {
      const result = await api<{ user: Profile }>("/api/auth/login", {
        method: "POST",
        body: JSON.stringify({ username, password }),
      });
      setProfile(result.user);
      setAuthenticated(true);
      setUsername("");
      setPassword("");
      const preferences = await api<{ hidden_cards: string[] }>("/api/me/preferences");
      setHiddenCards(preferences.hidden_cards);
      if (result.user.role === "admin") {
        setProfiles(await api<Profile[]>("/api/admin/users"));
      }
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Sign in failed");
    }
  }

  async function savePreferences(cardID: string, visible: boolean) {
    const nextHidden = visible
      ? hiddenCards.filter((card) => card !== cardID)
      : [...hiddenCards, cardID];
    try {
      const result = await api<{ hidden_cards: string[] }>("/api/me/preferences", {
        method: "PUT",
        body: JSON.stringify({ hidden_cards: nextHidden }),
      });
      setHiddenCards(result.hidden_cards);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not save dashboard preferences");
    }
  }

  async function createProfile(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setNotice("");
    try {
      await api("/api/admin/users", {
        method: "POST",
        body: JSON.stringify({ username: newUsername, password: newPassword }),
      });
      setNewUsername("");
      setNewPassword("");
      setProfiles(await api<Profile[]>("/api/admin/users"));
      setNotice("Household profile created.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not create profile");
    }
  }

  async function changePassword(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setNotice("");
    try {
      await api("/api/me/password", {
        method: "POST",
        body: JSON.stringify({ current_password: currentPassword, new_password: replacementPassword }),
      });
      setCurrentPassword("");
      setReplacementPassword("");
      setNotice("Password changed.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not change password");
    }
  }

  async function connectAccount() {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      if (!window.Plaid) throw new Error("Plaid Link did not load. Check your network and reload.");
      const { link_token: linkToken } = await api<{ link_token: string }>("/api/plaid/link-token", { method: "POST" });
      const handler = window.Plaid.create({
        token: linkToken,
        onSuccess: async (publicToken) => {
          try {
            await api("/api/plaid/exchange", {
              method: "POST",
              body: JSON.stringify({ public_token: publicToken }),
            });
            setNotice("Your account is connected. Transactions are syncing.");
            await refresh();
          } catch (err) {
            setError(err instanceof Error ? err.message : "Could not finish connecting your account");
          } finally {
            setBusy(false);
          }
        },
        onExit: (err) => {
          if (err) setError("Plaid Link closed before the connection was completed.");
          setBusy(false);
        },
      });
      handler.open();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not start Plaid Link");
      setBusy(false);
    }
  }

  async function syncNow() {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await api("/api/plaid/sync", { method: "POST" });
      setNotice("Sync complete.");
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Sync failed");
    } finally {
      setBusy(false);
    }
  }

  async function signOut() {
    try {
      await api("/api/auth/logout", { method: "POST" });
      setAuthenticated(false);
      setProfile(null);
      setHiddenCards([]);
      setProfiles([]);
      setAccounts([]);
      setTransactions([]);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not sign out");
    }
  }

  if (authenticated === null) {
    return <main className="loading-screen">Loading your workspace…</main>;
  }

  if (!authenticated) {
    return (
      <main className="login-screen">
        <form className="login-card" onSubmit={signIn}>
          <div className="brand-mark"><Wallet size={21} /></div>
          <p className="eyebrow">SNAPPY FINANCE</p>
          <h1>Your money, in focus.</h1>
          <p className="muted">Sign in to your private finance dashboard.</p>
          <label htmlFor="username">Username</label>
          <input
            id="username"
            autoComplete="username"
            value={username}
            onChange={(event) => setUsername(event.target.value)}
            required
          />
          <label htmlFor="password">Password</label>
          <input
            id="password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            required
          />
          {error && <p className="error-message">{error}</p>}
          <button className="primary-button full-width" type="submit">Sign in</button>
          <p className="secure-note"><LockKeyhole size={14} /> Your data stays on your server.</p>
        </form>
      </main>
    );
  }

  return (
    <main className="app-shell">
      <aside className="sidebar">
        <div className="brand"><span className="brand-mark"><Wallet size={19} /></span><span>snappy<span className="brand-light">finance</span></span></div>
        <div className="nav-label">WORKSPACE</div>
        <a className="nav-item active" href="#overview"><Banknote size={18} /> Overview</a>
        <a className="nav-item" href="#accounts"><Landmark size={18} /> Accounts</a>
        <div className="sidebar-bottom">
          <div className="privacy-card"><LockKeyhole size={16} /><span>Private by design<br /><small>Your data is self-hosted</small></span></div>
          <div className="profile-identity">
            <strong>{profile?.username}</strong>
            <span>{profile?.role === "admin" ? "Administrator" : "Household profile"}</span>
          </div>
          <button className="signout-button" onClick={() => void signOut()}><LogOut size={16} /> Sign out</button>
        </div>
      </aside>

      <section className="main-content" id="overview">
        <header className="topbar">
          <div><p className="eyebrow">YOUR FINANCES</p><h1>Overview</h1></div>
          <div className="topbar-actions">
            {profile?.role === "admin" && <>
              <button className="secondary-button" onClick={() => void syncNow()} disabled={busy || !accounts.some((account) => !account.demo_data)}>
                <RefreshCw size={16} className={busy ? "spin" : ""} /> Sync
              </button>
              <button className="primary-button" onClick={() => void connectAccount()} disabled={busy}>
                <span>+</span> Connect account
              </button>
            </>}
          </div>
        </header>

        {accounts.some((account) => account.demo_data) && (
          <div className="alert demo-banner">Demo data — these fictional accounts and transactions are for local testing only.</div>
        )}
        {error && <div className="alert error-message">{error}</div>}
        {notice && <div className="alert success-message">{notice}</div>}

        <section className="panel settings-panel">
          <div className="panel-heading"><div><h2>Dashboard preferences</h2><p>Choose which summary cards appear on your dashboard.</p></div></div>
          <div className="preference-list">
            {dashboardCards.map((card) => (
              <label className="preference-option" key={card.id}>
                <input
                  type="checkbox"
                  checked={!hiddenCards.includes(card.id)}
                  onChange={(event) => void savePreferences(card.id, event.target.checked)}
                />
                {card.label}
              </label>
            ))}
          </div>
        </section>

        {profile?.role === "admin" && (
          <section className="panel settings-panel">
            <div className="panel-heading"><div><h2>Household profiles</h2><p>Profiles share financial data; each person has their own dashboard and password.</p></div></div>
            <div className="profile-list">
              {profiles.map((item) => (
                <div className="profile-row" key={item.id}>
                  <strong>{item.username}</strong>
                  <span>{item.role === "admin" ? "Administrator" : "Household user"}</span>
                </div>
              ))}
            </div>
            <form className="profile-form" onSubmit={createProfile}>
              <label>New username<input autoComplete="off" minLength={3} maxLength={32} value={newUsername} onChange={(event) => setNewUsername(event.target.value)} required /></label>
              <label>Temporary password<input type="password" autoComplete="new-password" minLength={12} maxLength={72} value={newPassword} onChange={(event) => setNewPassword(event.target.value)} required /></label>
              <button className="primary-button" type="submit">Create household profile</button>
            </form>
          </section>
        )}

        <details className="panel password-panel">
          <summary>Change my password</summary>
          <form className="profile-form" onSubmit={changePassword}>
            <label>Current password<input type="password" autoComplete="current-password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} required /></label>
            <label>New password<input type="password" autoComplete="new-password" minLength={12} maxLength={72} value={replacementPassword} onChange={(event) => setReplacementPassword(event.target.value)} required /></label>
            <button className="secondary-button" type="submit">Update password</button>
          </form>
        </details>

        <div className="summary-grid">
          {!hiddenCards.includes("balance") && <article className="summary-card balance-card">
            <div className="summary-label">TOTAL ACCOUNT BALANCE</div>
            <div className="summary-value">
              {money(accounts.reduce((sum, account) => sum + (account.current_balance ?? 0), 0))}
            </div>
            <div className="summary-foot"><Wallet size={15} /> Across {accounts.length} connected account{accounts.length === 1 ? "" : "s"}</div>
          </article>}
          {!hiddenCards.includes("income") && <article className="summary-card">
            <div className="summary-label">INCOME THIS MONTH</div>
            <div className="summary-value">{money(totals.income)}</div>
            <div className="summary-foot positive"><ArrowDownLeft size={15} /> Inflows</div>
          </article>}
          {!hiddenCards.includes("spending") && <article className="summary-card">
            <div className="summary-label">SPENDING THIS MONTH</div>
            <div className="summary-value">{money(totals.spent)}</div>
            <div className="summary-foot"><ArrowUpRight size={15} /> Outflows</div>
          </article>}
          {!hiddenCards.includes("net") && <article className="summary-card">
            <div className="summary-label">MONTHLY NET</div>
            <div className={`summary-value ${totals.net >= 0 ? "positive-text" : "negative-text"}`}>{money(totals.net)}</div>
            <div className="summary-foot">Income minus spending</div>
          </article>}
        </div>

        <section className="panel" id="accounts">
          <div className="panel-heading"><div><h2>Accounts</h2><p>Balances from your connected institutions</p></div></div>
          {loading ? <div className="empty-state">Loading accounts…</div> : accounts.length === 0 ? (
            <div className="empty-state">
              <span className="empty-icon"><Landmark size={22} /></span>
              <strong>No accounts connected yet</strong>
              <span>{profile?.role === "admin" ? "Connect a bank to see your balances and transactions." : "Ask your household administrator to connect a bank account."}</span>
              {profile?.role === "admin" && <button className="primary-button" onClick={() => void connectAccount()} disabled={busy}>Connect your first account</button>}
            </div>
          ) : (
            <div className="account-list">
              {accounts.map((account) => (
                <div className="account-row" key={account.account_id}>
                  <span className="account-icon"><Landmark size={18} /></span>
                  <div className="account-info"><strong>{account.official_name || account.name}</strong><span>{account.type}{account.subtype ? ` · ${account.subtype}` : ""}{account.mask ? ` ···· ${account.mask}` : ""}{account.demo_data ? " · DEMO" : ""}</span></div>
                  <strong className="account-balance">{money(account.current_balance, account.iso_currency_code)}</strong>
                </div>
              ))}
            </div>
          )}
        </section>

        <section className="panel">
          <div className="panel-heading"><div><h2>Recent transactions</h2><p>Your latest activity across accounts</p></div></div>
          {loading ? <div className="empty-state">Loading transactions…</div> : transactions.length === 0 ? (
            <div className="empty-state compact">Transactions will appear here after your first account sync.</div>
          ) : (
            <div className="transaction-list">
              {transactions.map((item) => (
                <div className="transaction-row" key={item.transaction_id}>
                  <span className={`transaction-icon ${item.amount < 0 ? "income-icon" : ""}`}>{item.amount < 0 ? <ArrowDownLeft size={17} /> : <ArrowUpRight size={17} />}</span>
                  <div className="transaction-info"><strong>{item.merchant_name || item.name}</strong><span>{item.category_detailed || item.category_primary || "Uncategorized"} · {new Date(`${item.date}T12:00:00`).toLocaleDateString()}</span></div>
                  <strong className={item.amount < 0 ? "positive-text" : ""}>{item.amount < 0 ? "+" : "−"}{money(Math.abs(item.amount), item.iso_currency_code)}{item.pending && <small className="pending"> Pending</small>}</strong>
                </div>
              ))}
            </div>
          )}
        </section>
        <footer>Snappy Finance · Self-hosted personal finance</footer>
      </section>
    </main>
  );
}
