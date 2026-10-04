(ns kmet-compat.before-agent-start
  (:require #?(:lg [pig.extension :as ext]
               :default [kmet.extension :as ext])))

(def log (atom []))

(defn init [api]
  (ext/on-before-agent-start
   api
   (fn [{:keys [prompt system-prompt]}]
     (swap! log conj (str "prompt " prompt))
     {:system-prompt (str system-prompt " +compat")})))
