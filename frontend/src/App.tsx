import { useCallback, useEffect, useMemo, useState } from "react";
import { ArrowDownLeft, ArrowUpRight, Banknote, Landmark, LockKeyhole, LogOut, RefreshCw, Wallet } from "lucide-react";

type Account = {
  account_id: string;
  name: string;
  official_name: string | null;
  mask: string | null;
  type: string;
  subtype: string;
  current_balance: number | null;
  iso_currency_code: string | null;
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
  const [password, setPassword] = useState("");
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
    api<{ authenticated: boolean }>("/api/auth/session")
      .then(({ authenticated: value }) => {
        setAuthenticated(value);
        if (value) void refresh();
      })
      .catch(() => {
        setAuthenticated(false);
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
      await api("/api/auth/login", { method: "POST", body: JSON.stringify({ password }) });
      setAuthenticated(true);
      setPassword("");
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Sign in failed");
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
    await api("/api/auth/logout", { method: "POST" });
    setAuthenticated(false);
    setAccounts([]);
    setTransactions([]);
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
          <label htmlFor="password">App password</label>
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
          <button className="signout-button" onClick={() => void signOut()}><LogOut size={16} /> Sign out</button>
        </div>
      </aside>

      <section className="main-content" id="overview">
        <header className="topbar">
          <div><p className="eyebrow">YOUR FINANCES</p><h1>Overview</h1></div>
          <div className="topbar-actions">
            <button className="secondary-button" onClick={() => void syncNow()} disabled={busy || accounts.length === 0}>
              <RefreshCw size={16} className={busy ? "spin" : ""} /> Sync
            </button>
            <button className="primary-button" onClick={() => void connectAccount()} disabled={busy}>
              <span>+</span> Connect account
            </button>
          </div>
        </header>

        {error && <div className="alert error-message">{error}</div>}
        {notice && <div className="alert success-message">{notice}</div>}

        <div className="summary-grid">
          <article className="summary-card balance-card">
            <div className="summary-label">TOTAL ACCOUNT BALANCE</div>
            <div className="summary-value">
              {money(accounts.reduce((sum, account) => sum + (account.current_balance ?? 0), 0))}
            </div>
            <div className="summary-foot"><Wallet size={15} /> Across {accounts.length} connected account{accounts.length === 1 ? "" : "s"}</div>
          </article>
          <article className="summary-card">
            <div className="summary-label">INCOME THIS MONTH</div>
            <div className="summary-value">{money(totals.income)}</div>
            <div className="summary-foot positive"><ArrowDownLeft size={15} /> Inflows</div>
          </article>
          <article className="summary-card">
            <div className="summary-label">SPENDING THIS MONTH</div>
            <div className="summary-value">{money(totals.spent)}</div>
            <div className="summary-foot"><ArrowUpRight size={15} /> Outflows</div>
          </article>
          <article className="summary-card">
            <div className="summary-label">MONTHLY NET</div>
            <div className={`summary-value ${totals.net >= 0 ? "positive-text" : "negative-text"}`}>{money(totals.net)}</div>
            <div className="summary-foot">Income minus spending</div>
          </article>
        </div>

        <section className="panel" id="accounts">
          <div className="panel-heading"><div><h2>Accounts</h2><p>Balances from your connected institutions</p></div></div>
          {loading ? <div className="empty-state">Loading accounts…</div> : accounts.length === 0 ? (
            <div className="empty-state">
              <span className="empty-icon"><Landmark size={22} /></span>
              <strong>No accounts connected yet</strong>
              <span>Connect a bank to see your balances and transactions.</span>
              <button className="primary-button" onClick={() => void connectAccount()} disabled={busy}>Connect your first account</button>
            </div>
          ) : (
            <div className="account-list">
              {accounts.map((account) => (
                <div className="account-row" key={account.account_id}>
                  <span className="account-icon"><Landmark size={18} /></span>
                  <div className="account-info"><strong>{account.official_name || account.name}</strong><span>{account.type}{account.subtype ? ` · ${account.subtype}` : ""}{account.mask ? ` ···· ${account.mask}` : ""}</span></div>
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
