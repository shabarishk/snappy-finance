import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ArrowDownLeft, ArrowLeftRight, ArrowUpRight, Banknote, ChevronDown, Eye, EyeOff, Filter, Landmark, LockKeyhole, LogOut, Menu, RefreshCw, UsersRound, Wallet, X } from "lucide-react";

type Profile = {
  id: string;
  username: string;
  role: "admin" | "user";
};

const dashboardCards = [
  { id: "income", label: "Income summary" },
  { id: "balance", label: "Total cash account balance" },
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

const accountCategories = [
  { type: "depository", label: "Cash accounts" },
  { type: "credit", label: "Credit accounts" },
  { type: "loan", label: "Loan accounts" },
];

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
  hidden_from_users?: boolean;
  category_id: string | null;
};

type TransactionTabID = "depository" | "credit" | "loan" | "other";

type TransactionCategory = {
  category_id: string;
  label: string;
  category_type: "income" | "expense";
  excluded_from_totals: boolean;
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

function transactionDateKey(date: Date) {
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

function monthKey(date: Date) {
  return transactionDateKey(date).slice(0, 7);
}

function getCurrentSpendMonth(today = new Date()) {
  const month = today.getDate() >= 4
    ? new Date(today.getFullYear(), today.getMonth(), 1)
    : new Date(today.getFullYear(), today.getMonth() - 1, 1);
  return monthKey(month);
}

function getInitialMonthHistoryStart(today = new Date()) {
  return monthKey(new Date(today.getFullYear(), today.getMonth() - 12, 1));
}

function getStatisticsRange(period: "spend" | "calendar", selectedMonth: string) {
  const [year, month] = selectedMonth.split("-").map(Number);
  const start = new Date(year, month - 1, period === "spend" ? 4 : 1);
  const end = period === "calendar"
    ? new Date(year, month, 0)
    : new Date(year, month, 3);
  const formatDate = (date: Date) => date.toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  });
  return {
    start: transactionDateKey(start),
    end: transactionDateKey(end),
    label: `${formatDate(start)} – ${formatDate(end)}`,
  };
}

export default function App() {
  const [authenticated, setAuthenticated] = useState<boolean | null>(null);
  const [profile, setProfile] = useState<Profile | null>(null);
  const [activePage, setActivePage] = useState<"overview" | "administration" | "transactions">(() => {
    if (window.location.hash === "#administration") return "administration";
    if (window.location.hash === "#transactions") return "transactions";
    return "overview";
  });
  const [adminSection, setAdminSection] = useState<"users" | "finance">("users");
  const [incomeDisplay, setIncomeDisplay] = useState<"expendable" | "total">("expendable");
  const [statisticsPeriod, setStatisticsPeriod] = useState<"spend" | "calendar">("spend");
  const [selectedOverviewMonth, setSelectedOverviewMonth] = useState(() => getCurrentSpendMonth());
  const [monthHistoryStart, setMonthHistoryStart] = useState(() => getInitialMonthHistoryStart());
  const [monthPickerOpen, setMonthPickerOpen] = useState(false);
  const [transactionTab, setTransactionTab] = useState<TransactionTabID>("depository");
  const [transactionPage, setTransactionPage] = useState(1);
  const [transactionPageSize, setTransactionPageSize] = useState(25);
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [dashboardPreferencesOpen, setDashboardPreferencesOpen] = useState(false);
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
  const [transactionCategories, setTransactionCategories] = useState<TransactionCategory[]>([]);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const monthPickerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const syncPageFromHash = () => {
      if (window.location.hash === "#administration") setActivePage("administration");
      else if (window.location.hash === "#transactions") setActivePage("transactions");
      else setActivePage("overview");
    };
    window.addEventListener("hashchange", syncPageFromHash);
    return () => window.removeEventListener("hashchange", syncPageFromHash);
  }, []);

  useEffect(() => {
    if (activePage !== "overview") setDashboardPreferencesOpen(false);
  }, [activePage]);

  useEffect(() => {
    const storageKey = "snappy-finance-overview-month-history-start";
    const validMonth = /^\d{4}-(0[1-9]|1[0-2])$/;
    try {
      const storedMonth = window.localStorage.getItem(storageKey);
      if (storedMonth && validMonth.test(storedMonth)) {
        if (storedMonth < monthHistoryStart) setMonthHistoryStart(storedMonth);
        else if (storedMonth > monthHistoryStart) window.localStorage.setItem(storageKey, monthHistoryStart);
      } else {
        window.localStorage.setItem(storageKey, monthHistoryStart);
      }
    } catch (err) {
      console.error("Could not persist Overview month picker history.", err);
      setError("Could not save the Overview month picker history on this device.");
    }
  }, [monthHistoryStart]);

  useEffect(() => {
    if (!monthPickerOpen) return;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") setMonthPickerOpen(false);
    };
    const closeOnOutsideClick = (event: PointerEvent) => {
      if (event.target instanceof Node && !monthPickerRef.current?.contains(event.target)) {
        setMonthPickerOpen(false);
      }
    };
    window.addEventListener("keydown", closeOnEscape);
    document.addEventListener("pointerdown", closeOnOutsideClick);
    return () => {
      window.removeEventListener("keydown", closeOnEscape);
      document.removeEventListener("pointerdown", closeOnOutsideClick);
    };
  }, [monthPickerOpen]);

  useEffect(() => {
    if (!sidebarOpen && !dashboardPreferencesOpen) return;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setSidebarOpen(false);
        setDashboardPreferencesOpen(false);
      }
    };
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    window.addEventListener("keydown", closeOnEscape);
    return () => {
      document.body.style.overflow = previousOverflow;
      window.removeEventListener("keydown", closeOnEscape);
    };
  }, [dashboardPreferencesOpen, sidebarOpen]);

  function navigateToPage(page: "overview" | "administration" | "transactions") {
    setActivePage(page);
    setSidebarOpen(false);
    setDashboardPreferencesOpen(false);
    setMonthPickerOpen(false);
    if (window.location.hash !== `#${page}`) window.location.hash = page;
  }

  const refresh = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const [accountRows, transactionRows, categoryRows] = await Promise.all([
        api<Account[]>("/api/accounts"),
        api<Transaction[]>("/api/transactions"),
        api<TransactionCategory[]>("/api/transaction-categories"),
      ]);
      setAccounts(accountRows);
      setTransactions(transactionRows);
      setTransactionCategories(categoryRows);
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

  const statisticsRange = useMemo(
    () => getStatisticsRange(statisticsPeriod, selectedOverviewMonth),
    [selectedOverviewMonth, statisticsPeriod],
  );
  const latestOverviewMonth = monthKey(new Date());
  const overviewMonthOptions = useMemo(() => {
    const startMonth = monthHistoryStart > latestOverviewMonth ? latestOverviewMonth : monthHistoryStart;
    const [startYear, startMonthNumber] = startMonth.split("-").map(Number);
    const [endYear, endMonthNumber] = latestOverviewMonth.split("-").map(Number);
    const months: string[] = [];
    let year = startYear;
    let month = startMonthNumber;
    while (year < endYear || (year === endYear && month <= endMonthNumber)) {
      months.push(`${year}-${String(month).padStart(2, "0")}`);
      if (month === 12) {
        year += 1;
        month = 1;
      } else {
        month += 1;
      }
    }
    return months.reverse();
  }, [latestOverviewMonth, monthHistoryStart]);
  const displayedMonth = new Date(
    Number(selectedOverviewMonth.slice(0, 4)),
    Number(selectedOverviewMonth.slice(5, 7)) - 1,
    1,
  );
  const displayedMonthName = displayedMonth.toLocaleDateString(undefined, { month: "long" });

  const totals = useMemo(() => {
    const categoriesByID = new Map(transactionCategories.map((category) => [category.category_id, category]));
    const current = transactions.filter(
      (item) => {
        if (item.date < statisticsRange.start || item.date > statisticsRange.end) return false;
        const category = item.category_id ? categoriesByID.get(item.category_id) : undefined;
        const transactionType = item.amount < 0 ? "income" : "expense";
        return !(category?.category_type === transactionType && category.excluded_from_totals);
      },
    );
    const spent = current.filter((item) => item.amount > 0).reduce((sum, item) => sum + item.amount, 0);
    const income = current.filter((item) => item.amount < 0).reduce((sum, item) => sum + Math.abs(item.amount), 0);
    const expendableIncome = current
      .filter((item) => item.amount < 0 && item.category_id === "expendable_income")
      .reduce((sum, item) => sum + Math.abs(item.amount), 0);
    return { spent, income, expendableIncome, net: income - spent };
  }, [transactions, transactionCategories, statisticsRange]);

  const accountGroups = useMemo(() => {
    const groups = accountCategories.map(({ type, label }) => ({
      label,
      accounts: accounts.filter((account) => account.type.toLowerCase() === type),
    }));
    const otherAccounts = accounts.filter(
      (account) => !accountCategories.some(({ type }) => account.type.toLowerCase() === type),
    );
    if (otherAccounts.length > 0) groups.push({ label: "Other accounts", accounts: otherAccounts });
    return groups;
  }, [accounts]);
  const cashAccounts = useMemo(
    () => accounts.filter((account) => account.type.toLowerCase() === "depository"),
    [accounts],
  );
  const totalCashBalance = cashAccounts.reduce((sum, account) => sum + (account.current_balance ?? 0), 0);

  const transactionTabs = useMemo(() => {
    const accountTypes = new Map(accounts.map((account) => [account.account_id, account.type.toLowerCase()]));
    const knownTypes = new Set(["depository", "credit", "loan"]);
    const hasOtherTransactions = transactions.some((item) => !knownTypes.has(accountTypes.get(item.account_id) ?? ""));
    const hasOtherAccounts = accounts.some((account) => !knownTypes.has(account.type.toLowerCase()));
    return [
      { id: "depository" as const, label: "Cash accounts" },
      { id: "credit" as const, label: "Credit accounts" },
      { id: "loan" as const, label: "Loan accounts" },
      ...(hasOtherTransactions || hasOtherAccounts ? [{ id: "other" as const, label: "Other accounts" }] : []),
    ];
  }, [accounts, transactions]);
  const activeTransactionTab = transactionTabs.some((tab) => tab.id === transactionTab)
    ? transactionTab
    : "depository";
  const visibleTransactions = useMemo(() => {
    const accountTypes = new Map(accounts.map((account) => [account.account_id, account.type.toLowerCase()]));
    return transactions.filter((item) => {
      const type = accountTypes.get(item.account_id) ?? "";
      return activeTransactionTab === "other"
        ? !["depository", "credit", "loan"].includes(type)
        : type === activeTransactionTab;
    });
  }, [accounts, activeTransactionTab, transactions]);
  const transactionPageCount = Math.max(1, Math.ceil(visibleTransactions.length / transactionPageSize));
  const currentTransactionPage = Math.min(transactionPage, transactionPageCount);
  const pageTransactions = useMemo(() => {
    const start = (currentTransactionPage - 1) * transactionPageSize;
    return visibleTransactions.slice(start, start + transactionPageSize);
  }, [currentTransactionPage, transactionPageSize, visibleTransactions]);
  const firstTransactionNumber = visibleTransactions.length === 0
    ? 0
    : (currentTransactionPage - 1) * transactionPageSize + 1;
  const lastTransactionNumber = Math.min(currentTransactionPage * transactionPageSize, visibleTransactions.length);

  const pageTitle = activePage === "administration"
    ? "Administration"
    : activePage === "transactions" ? "Transactions" : "Overview";
  const pageEyebrow = activePage === "administration" ? "ADMINISTRATION" : "YOUR FINANCES";
  const currentSpendMonth = getCurrentSpendMonth();
  const currentPeriodLabel = statisticsPeriod === "spend"
    ? selectedOverviewMonth === currentSpendMonth ? "THIS CYCLE" : "SPEND CYCLE"
    : selectedOverviewMonth === latestOverviewMonth ? "THIS MONTH" : "SELECTED MONTH";

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

  async function setTransactionVisibility(item: Transaction) {
    const hiddenFromUsers = !item.hidden_from_users;
    setError("");
    setNotice("");
    try {
      await api("/api/admin/transactions/visibility", {
        method: "PATCH",
        body: JSON.stringify({
          transaction_id: item.transaction_id,
          hidden_from_users: hiddenFromUsers,
        }),
      });
      setTransactions((current) => current.map((transaction) =>
        transaction.transaction_id === item.transaction_id
          ? { ...transaction, hidden_from_users: hiddenFromUsers }
          : transaction,
      ));
      setNotice(hiddenFromUsers
        ? "Transaction hidden from household users."
        : "Transaction visible to household users.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not update transaction visibility");
    }
  }

  async function setTransactionCategory(item: Transaction, categoryID: string) {
    setError("");
    setNotice("");
    try {
      const result = await api<{
        category_id: string | null;
        matched_transaction_ids: string[];
        matching_rule_applied: boolean;
      }>("/api/admin/transactions/category", {
        method: "PATCH",
        body: JSON.stringify({ transaction_id: item.transaction_id, category_id: categoryID }),
      });
      const matchedIDs = new Set(result.matched_transaction_ids);
      setTransactions((current) => current.map((transaction) =>
        matchedIDs.has(transaction.transaction_id)
          ? { ...transaction, category_id: result.category_id }
          : transaction,
      ));
      const label = transactionCategories.find((category) => category.category_id === categoryID)?.label;
      if (!categoryID) {
        setNotice("Using the bank category for this transaction. Similar transactions remain categorized automatically.");
      } else if (result.matching_rule_applied) {
        const count = result.matched_transaction_ids.length;
        const matchScope = item.amount < 0 ? " in this account" : "";
        setNotice(`Applied ${label ?? "Other"} to ${count} matching transaction${count === 1 ? "" : "s"}. Future matches${matchScope} will use this category.`);
      } else {
        setNotice(`Categorized this transaction as ${label ?? "Other"}, but no stable description was available to categorize similar transactions.`);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not categorize transaction");
    }
  }

  async function setCategoryExcluded(category: TransactionCategory, excluded: boolean) {
    setError("");
    setNotice("");
    try {
      await api(`/api/admin/transaction-categories/${encodeURIComponent(category.category_id)}`, {
        method: "PATCH",
        body: JSON.stringify({ excluded_from_totals: excluded }),
      });
      setTransactionCategories((current) => current.map((item) =>
        item.category_id === category.category_id
          ? { ...item, excluded_from_totals: excluded }
          : item,
      ));
      const totalName = category.category_type === "income" ? "income" : "spending";
      setNotice(excluded
        ? `${category.label} transactions excluded from ${totalName} for all profiles.`
        : `${category.label} transactions included in ${totalName} for all profiles.`);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not update category settings");
    }
  }

  const transactionsPanel = (
    <section className="panel" id="transactions">
      <div className="panel-heading"><div><h2>Recent transactions</h2><p>{profile?.role === "admin" ? "Categories apply to similar past and future transactions; manage exclusions in Administration." : "Your latest activity across accounts"}</p></div></div>
      <div className="transactions-layout">
        <div className="transaction-tabs" role="group" aria-label="Filter transactions by account type">
          {transactionTabs.map((tab) => (
            <button
              type="button"
              key={tab.id}
              className={`transaction-tab ${activeTransactionTab === tab.id ? "active" : ""}`}
              aria-pressed={activeTransactionTab === tab.id}
              onClick={() => {
                setTransactionTab(tab.id);
                setTransactionPage(1);
              }}
            >{tab.label}</button>
          ))}
        </div>
        <div className="transactions-content">
          <div className="transaction-list-toolbar">
            <span>Showing {firstTransactionNumber}–{lastTransactionNumber} of {visibleTransactions.length}</span>
            <label className="page-size-control">
              Items per page
              <select
                value={transactionPageSize}
                onChange={(event) => {
                  setTransactionPageSize(Number(event.target.value));
                  setTransactionPage(1);
                }}
                aria-label="Transactions per page"
              >
                {[25, 50, 75, 100].map((pageSize) => (
                  <option key={pageSize} value={pageSize}>{pageSize}</option>
                ))}
              </select>
            </label>
          </div>
          {loading ? <div className="empty-state">Loading transactions…</div> : visibleTransactions.length === 0 ? (
            <div className="empty-state compact">
              {transactions.length === 0
                ? "Transactions will appear here after your first account sync."
                : `No transactions for ${transactionTabs.find((tab) => tab.id === activeTransactionTab)?.label.toLowerCase() ?? "this account type"} yet.`}
            </div>
          ) : (
            <div className="transaction-list">
              {pageTransactions.map((item) => (
                <div className={`transaction-row ${item.hidden_from_users ? "hidden-transaction" : ""}`} key={item.transaction_id}>
                  <span className={`transaction-icon ${item.amount < 0 ? "income-icon" : ""}`}>{item.amount < 0 ? <ArrowDownLeft size={17} /> : <ArrowUpRight size={17} />}</span>
                  <div className="transaction-info"><strong>{item.merchant_name || item.name}</strong><span>{transactionCategories.find((category) => category.category_id === item.category_id)?.label || item.category_detailed || item.category_primary || "Uncategorized"} · {new Date(`${item.date}T12:00:00`).toLocaleDateString()}</span></div>
                  <strong className={item.amount < 0 ? "positive-text" : ""}>{item.amount < 0 ? "+" : "−"}{money(Math.abs(item.amount), item.iso_currency_code)}{item.pending && <small className="pending"> Pending</small>}</strong>
                  {profile?.role === "admin" && (
                    <div className="transaction-actions">
                      <select
                        className="category-select"
                        value={item.category_id ?? ""}
                        aria-label={`Category for ${item.merchant_name || item.name}`}
                        onChange={(event) => void setTransactionCategory(item, event.target.value)}
                      >
                        <option value="">Bank category</option>
                        {transactionCategories.filter((category) => category.category_type === (item.amount < 0 ? "income" : "expense")).map((category) => (
                          <option key={category.category_id} value={category.category_id}>{category.label}</option>
                        ))}
                      </select>
                      <button
                        type="button"
                        className="visibility-button"
                        onClick={() => void setTransactionVisibility(item)}
                        aria-label={item.hidden_from_users ? "Show transaction to household users" : "Hide transaction from household users"}
                        title={item.hidden_from_users ? "Show to household users" : "Hide from household users"}
                      >
                        {item.hidden_from_users ? <Eye size={15} /> : <EyeOff size={15} />}
                        <span>{item.hidden_from_users ? "Show" : "Hide"}</span>
                      </button>
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}
          <nav className="transaction-pagination" aria-label="Transaction pages">
            <button
              type="button"
              className="secondary-button"
              disabled={currentTransactionPage <= 1}
              onClick={() => setTransactionPage(currentTransactionPage - 1)}
            >Previous</button>
            <span>Page {currentTransactionPage} of {transactionPageCount}</span>
            <button
              type="button"
              className="secondary-button"
              disabled={currentTransactionPage >= transactionPageCount}
              onClick={() => setTransactionPage(currentTransactionPage + 1)}
            >Next</button>
          </nav>
        </div>
      </div>
    </section>
  );

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
      setIncomeDisplay("expendable");
      navigateToPage("overview");
      setAdminSection("users");
      setHiddenCards([]);
      setProfiles([]);
      setAccounts([]);
      setTransactions([]);
      setTransactionCategories([]);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not sign out");
    }
  }

  const passwordPanel = (
    <details className="panel password-panel">
      <summary>Change my password</summary>
      <form className="profile-form" onSubmit={changePassword}>
        <label>Current password<input type="password" autoComplete="current-password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} required /></label>
        <label>New password<input type="password" autoComplete="new-password" minLength={12} maxLength={72} value={replacementPassword} onChange={(event) => setReplacementPassword(event.target.value)} required /></label>
        <button className="secondary-button" type="submit">Update password</button>
      </form>
    </details>
  );

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
      <aside className={`sidebar ${sidebarOpen ? "sidebar-open" : ""}`} id="app-sidebar">
        <div className="brand"><span className="brand-mark"><Wallet size={19} /></span><span>snappy<span className="brand-light">finance</span></span></div>
        <nav className="sidebar-nav" aria-label="Workspace">
          <div className="nav-label">WORKSPACE</div>
          <button
            className={`nav-item ${activePage === "overview" ? "active" : ""}`}
            onClick={() => navigateToPage("overview")}
            aria-current={activePage === "overview" ? "page" : undefined}
          ><Banknote size={18} /> Overview</button>
          <button
            className={`nav-item ${activePage === "transactions" ? "active" : ""}`}
            onClick={() => navigateToPage("transactions")}
            aria-current={activePage === "transactions" ? "page" : undefined}
          ><ArrowLeftRight size={18} /> Transactions</button>
        </nav>
        <div className="sidebar-bottom">
          <button
            className={`nav-item admin-nav-item ${activePage === "administration" ? "active" : ""}`}
            onClick={() => navigateToPage("administration")}
            aria-current={activePage === "administration" ? "page" : undefined}
          ><UsersRound size={18} /> Administration</button>
          <div className="privacy-card"><LockKeyhole size={16} /><span>Private by design<br /><small>Your data is self-hosted</small></span></div>
          <div className="profile-identity">
            <strong>{profile?.username}</strong>
            <span>{profile?.role === "admin" ? "Administrator" : "Household profile"}</span>
          </div>
          <button className="signout-button" onClick={() => void signOut()}><LogOut size={16} /> Sign out</button>
        </div>
      </aside>
      <div className="mobile-topbar" aria-hidden="true">
        <div className="brand"><span className="brand-mark"><Wallet size={19} /></span><span>snappy<span className="brand-light">finance</span></span></div>
      </div>
      <button
        type="button"
        className="menu-toggle"
        aria-label={sidebarOpen ? "Close navigation menu" : "Open navigation menu"}
        aria-expanded={sidebarOpen}
        aria-controls="app-sidebar"
        onClick={() => setSidebarOpen((open) => !open)}
      >{sidebarOpen ? <X size={20} /> : <Menu size={20} />}</button>
      <button
        type="button"
        className={`sidebar-backdrop ${sidebarOpen ? "visible" : ""}`}
        aria-label="Close navigation menu"
        aria-hidden={!sidebarOpen}
        tabIndex={sidebarOpen ? 0 : -1}
        onClick={() => setSidebarOpen(false)}
      />

      <section className="main-content">
        <header className="topbar">
          <div>
            <p className="eyebrow">{pageEyebrow}</p>
            {activePage === "overview" ? (
              <div className="overview-month-picker" ref={monthPickerRef}>
                <h1 className="overview-month-title">
                  <button
                    type="button"
                    className="overview-month-button"
                    aria-label={`${displayedMonthName} ${displayedMonth.getFullYear()} finances. Choose month.`}
                    aria-expanded={monthPickerOpen}
                    aria-haspopup="dialog"
                    aria-controls="overview-month-picker"
                    onClick={() => setMonthPickerOpen((open) => !open)}
                  >
                    <span>{displayedMonthName}'s finances</span>
                    <span className="overview-month-year">{displayedMonth.getFullYear()}</span>
                    <ChevronDown size={18} />
                  </button>
                </h1>
                {monthPickerOpen && (
                  <div id="overview-month-picker" className="month-picker-popover" role="dialog" aria-label="Choose a calendar month">
                    <label className="month-picker-control">
                      View month
                      <select
                        value={selectedOverviewMonth}
                        onChange={(event) => {
                          setSelectedOverviewMonth(event.target.value);
                          setStatisticsPeriod("calendar");
                          setMonthPickerOpen(false);
                        }}
                      >
                        {overviewMonthOptions.map((monthKey) => {
                          const [year, month] = monthKey.split("-").map(Number);
                          const label = new Date(year, month - 1, 1).toLocaleDateString(undefined, {
                            month: "long",
                            year: "numeric",
                          });
                          return <option key={monthKey} value={monthKey}>{label}</option>;
                        })}
                      </select>
                    </label>
                  </div>
                )}
              </div>
            ) : <h1>{pageTitle}</h1>}
          </div>
          <div className="topbar-actions">
            {activePage === "overview" && (
              <button
                type="button"
                className="secondary-button filter-button"
                aria-expanded={dashboardPreferencesOpen}
                aria-controls="dashboard-preferences"
                onClick={() => setDashboardPreferencesOpen(true)}
              >
                <Filter size={16} /> Filter
              </button>
            )}
            {activePage === "overview" && profile?.role === "admin" && <>
              <button className="secondary-button" onClick={() => void syncNow()} disabled={busy || !accounts.some((account) => !account.demo_data)}>
                <RefreshCw size={16} className={busy ? "spin" : ""} /> Sync
              </button>
              <button className="primary-button" onClick={() => void connectAccount()} disabled={busy}>
                <span>+</span> Connect account
              </button>
            </>}
          </div>
        </header>

        {error && <div className="alert error-message">{error}</div>}
        {notice && <div className="alert success-message">{notice}</div>}

        {activePage === "administration" ? (
          profile?.role === "admin" ? (
            <>
              <div className="admin-tabs" role="group" aria-label="Administration sections">
                <button
                  type="button"
                  className={`admin-tab ${adminSection === "users" ? "active" : ""}`}
                  aria-pressed={adminSection === "users"}
                  onClick={() => setAdminSection("users")}
                >User management</button>
                <button
                  type="button"
                  className={`admin-tab ${adminSection === "finance" ? "active" : ""}`}
                  aria-pressed={adminSection === "finance"}
                  onClick={() => setAdminSection("finance")}
                >Financial management</button>
              </div>
              {adminSection === "users" ? (
                <>
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
                  {passwordPanel}
                </>
              ) : (
                <section className="panel settings-panel">
                  <div className="panel-heading">
                    <div><h2>Monthly total exclusions</h2><p>Exclude categories from Spending or Income. Monthly Net uses the adjusted totals for every profile.</p></div>
                  </div>
                  <div className="category-exclusion-groups">
                    {(["expense", "income"] as const).map((categoryType) => (
                      <section className="category-exclusion-group" key={categoryType}>
                        <h3>{categoryType === "expense" ? "Spending" : "Income"}</h3>
                        <div className="preference-list">
                          {transactionCategories.filter((category) => category.category_type === categoryType).map((category) => (
                            <label className="preference-option" key={category.category_id}>
                              <input
                                type="checkbox"
                                checked={category.excluded_from_totals}
                                onChange={(event) => void setCategoryExcluded(category, event.target.checked)}
                              />
                              Exclude {category.label} from {categoryType === "expense" ? "spending" : "income"}
                            </label>
                          ))}
                        </div>
                      </section>
                    ))}
                  </div>
                </section>
              )}
            </>
          ) : passwordPanel
        ) : activePage === "transactions" ? (
          <>
            {accounts.some((account) => account.demo_data) && (
              <div className="alert demo-banner">Demo data — these fictional accounts and transactions are for local testing only.</div>
            )}
            {transactionsPanel}
          </>
        ) : (
          <>
            {accounts.some((account) => account.demo_data) && (
              <div className="alert demo-banner">Demo data — these fictional accounts and transactions are for local testing only.</div>
            )}
            <button
              type="button"
              className={`dashboard-preferences-backdrop ${dashboardPreferencesOpen ? "visible" : ""}`}
              aria-label="Close dashboard filters"
              aria-hidden={!dashboardPreferencesOpen}
              tabIndex={dashboardPreferencesOpen ? 0 : -1}
              onClick={() => setDashboardPreferencesOpen(false)}
            />
            <aside
              id="dashboard-preferences"
              className={`dashboard-preferences-drawer ${dashboardPreferencesOpen ? "open" : ""}`}
              aria-labelledby="dashboard-preferences-title"
              aria-hidden={!dashboardPreferencesOpen}
            >
              <section className="panel settings-panel">
                <div className="panel-heading preferences-drawer-heading">
                  <div><h2 id="dashboard-preferences-title">Dashboard preferences</h2><p>Choose which summary cards and statistics period appear.</p></div>
                  <button
                    type="button"
                    className="preferences-drawer-close"
                    aria-label="Close dashboard filters"
                    onClick={() => setDashboardPreferencesOpen(false)}
                  ><X size={18} /></button>
                </div>
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
                <div className="statistics-period-control">
                  <div className="statistics-period-copy">
                    <strong>Statistics period</strong>
                    <span>{statisticsPeriod === "spend" ? "Spend calendar (4th–3rd)" : "Calendar month"} · {statisticsRange.label}</span>
                  </div>
                  <div className="statistics-period-options" role="group" aria-label="Statistics period">
                    <button
                      type="button"
                      className={`statistics-period-option ${statisticsPeriod === "spend" ? "active" : ""}`}
                      aria-pressed={statisticsPeriod === "spend"}
                      onClick={() => setStatisticsPeriod("spend")}
                    >Spend calendar</button>
                    <button
                      type="button"
                      className={`statistics-period-option ${statisticsPeriod === "calendar" ? "active" : ""}`}
                      aria-pressed={statisticsPeriod === "calendar"}
                      onClick={() => setStatisticsPeriod("calendar")}
                    >Calendar month</button>
                  </div>
                </div>
              </section>
            </aside>

        <div className="summary-grid">
          {!hiddenCards.includes("income") && <article className="summary-card income-card">
            <div className="income-summary-heading">
              <div className="summary-label">{incomeDisplay === "expendable" ? "TOTAL EXPENDABLE INCOME" : `TOTAL INCOME ${currentPeriodLabel}`}</div>
              <button
                type="button"
                className="summary-toggle"
                onClick={() => setIncomeDisplay(incomeDisplay === "expendable" ? "total" : "expendable")}
              >View {incomeDisplay === "expendable" ? "total income" : "expendable income"}</button>
            </div>
            <div className="summary-value">{money(incomeDisplay === "expendable" ? totals.expendableIncome : totals.income)}</div>
            <div className="summary-foot positive"><ArrowDownLeft size={15} /> {incomeDisplay === "expendable" ? "Expendable inflows" : "All income inflows"}</div>
          </article>}
          {!hiddenCards.includes("balance") && <article className="summary-card balance-card">
            <div className="summary-label">TOTAL CASH ACCOUNT BALANCE</div>
            <div className="summary-value">
              {money(totalCashBalance)}
            </div>
            <div className="summary-foot"><Wallet size={15} /> Across {cashAccounts.length} cash account{cashAccounts.length === 1 ? "" : "s"}</div>
          </article>}
          {!hiddenCards.includes("spending") && <article className="summary-card">
            <div className="summary-label">SPENDING {currentPeriodLabel}</div>
            <div className="summary-value">{money(totals.spent)}</div>
            <div className="summary-foot"><ArrowUpRight size={15} /> Outflows</div>
          </article>}
          {!hiddenCards.includes("net") && <article className="summary-card">
            <div className="summary-label">NET {currentPeriodLabel}</div>
            <div className={`summary-value ${totals.net >= 0 ? "positive-text" : "negative-text"}`}>{money(totals.net)}</div>
            <div className="summary-foot">Income minus spending in selected period</div>
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
              {accountGroups.map((group) => (
                <section className="account-group" key={group.label}>
                  <div className="account-group-heading">
                    <h3>{group.label}</h3>
                    <span>{group.accounts.length}</span>
                  </div>
                  {group.accounts.length === 0 ? (
                    <div className="account-group-empty">No {group.label.toLowerCase()} connected.</div>
                  ) : group.accounts.map((account) => (
                    <div className="account-row" key={account.account_id}>
                      <span className="account-icon"><Landmark size={18} /></span>
                      <div className="account-info"><strong>{account.official_name || account.name}</strong><span>{account.type}{account.subtype ? ` · ${account.subtype}` : ""}{account.mask ? ` ···· ${account.mask}` : ""}{account.demo_data ? " · DEMO" : ""}</span></div>
                      <strong className="account-balance">{money(account.current_balance, account.iso_currency_code)}</strong>
                    </div>
                  ))}
                </section>
              ))}
            </div>
          )}
        </section>

          </>
        )}
        <footer>Snappy Finance · Self-hosted personal finance</footer>
      </section>
    </main>
  );
}
