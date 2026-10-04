(ns portable.entry
  (:require #?(:lg [pig.extension :as ext]
               :default [kmet.extension :as ext])
            [portable.core :as core]))

(defn init [api]
  (ext/register-tool!
   api
   {:name "classify"
    :description "Report portable text facts"
    :parameters {:type "object"
                 :properties {:text {:type "string"}}
                 :required ["text"]}
    :execute (fn [{:keys [text]}]
               {:content [{:type "text" :text (core/report text)}]
                :details {:host (name core/host)}})}))
