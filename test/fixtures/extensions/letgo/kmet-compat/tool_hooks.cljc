(ns kmet-compat.tool-hooks
  (:require #?(:lg [pig.extension :as ext]
               :default [kmet.extension :as ext])))

(def log (atom []))

(defn init [api]
  (ext/on-tool-call
   api
   (fn [{:keys [tool-name]}]
     (when (= tool-name "bash")
       {:block true :reason "no bash"})))
  (ext/on-tool-result
   api
   (fn [{:keys [tool-name is-error]}]
     (swap! log conj (str tool-name ":" is-error))
     (when (= tool-name "bash")
       {:is-error true}))))
