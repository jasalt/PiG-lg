;; Runs the kmet-compat fixtures against Kmet's create-nullable-api and compares each outcome with expected.json,
;; the same file the let-go test (coding/extension/host/letgo/kmet_compat_test.go) asserts through inproc.Runner.
;; Run it through run-kmet.sh. Babashka only; Kmet is not vendored and nothing here is part of CI.
(require '[cheshire.core :as json]
         '[clojure.java.io :as io]
         '[kmet.extension :as kmet])

(def here (System/getProperty "user.dir"))

(def expected (json/parse-string (slurp (io/file here "expected.json")) true))

(defn load-fixture
  "Loads one fixture file against a fresh nullable api and calls its init."
  [file ns-sym]
  (let [{:keys [api state]} (kmet/create-nullable-api)]
    (load-file (str (io/file here file)))
    ((requiring-resolve (symbol (str ns-sym) "init")) api)
    {:state state :log (requiring-resolve (symbol (str ns-sym) "log"))}))

(defn- hook-results [hooks event] (mapv #(% event) hooks))

(def scenarios
  {:tool-only
   (fn []
     (let [{:keys [state log]} (load-fixture "tool_only.cljc" 'kmet-compat.tool-only)
           result ((:execute (get-in @state [:tools "echo"])) {:text "hi"})]
       {:result (:content result) :log @@log}))

   :command
   (fn []
     (let [{:keys [state log]} (load-fixture "command.cljc" 'kmet-compat.command)
           command (get-in @state [:commands "greet"])]
       ((:handler command) {} "Ada")
       {:description (:description command) :log @@log}))

   :session-start
   (fn []
     (let [{:keys [state log]} (load-fixture "session_start.cljc" 'kmet-compat.session-start)]
       (doseq [handler (get-in @state [:handlers :session-start])]
         (handler {:reason :startup} {}))
       {:log @@log}))

   :before-agent-start
   (fn []
     (let [{:keys [state log]} (load-fixture "before_agent_start.cljc" 'kmet-compat.before-agent-start)
           [result] (hook-results (:before-agent-start-hooks @state) {:prompt "do it" :system-prompt "base"})]
       {:system-prompt (:system-prompt result) :log @@log}))

   :tool-hooks
   (fn []
     (let [{:keys [state log]} (load-fixture "tool_hooks.cljc" 'kmet-compat.tool-hooks)
           call (fn [tool] (first (hook-results (:tool-call-hooks @state) {:tool-name tool :args {}})))
           result (fn [tool] (first (hook-results (:tool-result-hooks @state) {:tool-name tool :is-error false})))]
       {:bash-call (call "bash") :read-call (call "read")
        :bash-result-is-error (:is-error (result "bash")) :read-result-is-error (:is-error (result "read"))
        :log @@log}))

   :helper-entry
   (fn []
     (let [{:keys [state]} (load-fixture "helper_entry.cljc" 'kmet-compat.helper-entry)
           result ((:execute (get-in @state [:tools "describe"])) {:text "the quick brown fox jumps"})]
       {:result (:content result)}))})

(defn normalize [value] (json/parse-string (json/generate-string value) true))

(def failures
  (doall
   (for [[name scenario] (sort-by key scenarios)
         :let [actual (try (normalize (scenario)) (catch Throwable e {:error (ex-message e)}))
               want (get expected name)]]
     (if (= actual want)
       (do (println "PASS" (clojure.core/name name)) nil)
       (do (println "FAIL" (clojure.core/name name) "\n  got " (pr-str actual) "\n  want" (pr-str want)) name)))))

(System/exit (if (some some? failures) 1 0))
