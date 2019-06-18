module HealthModel
    class HealthModelDefinition

        attr_reader :health_model_definition

        def initialize(definition)
            @health_model_definition = definition
        end

        # gets the parent monitor id given the state transition. It requires the monitor id and labels to determine the parent id
        def get_parent_monitor_id(monitor)
            monitor_id = monitor.monitor_id

            if @health_model_definition.key?(monitor_id)
                parent_monitor_id = @health_model_definition[monitor_id]['parent_monitor_id']
                # check parent_monitor_id is an array, then evaluate the conditions, else return the parent_monitor_id
                if parent_monitor_id.is_a?(String)
                    return parent_monitor_id
                end
                if parent_monitor_id.nil?
                    conditions = @health_model_definition[monitor_id]['conditions']
                    if !conditions.nil? && conditions.is_a?(Array)
                        labels = monitor.labels
                        conditions.each{|condition|
                            left = "#{labels[condition['key']]}"
                            op = "#{condition['operator']}"
                            right = "#{condition['value']}"
                            cond = left.send(op.to_sym, right)

                            if cond
                                return condition['parent_id']
                            end

                        }
                    end
                end
            else
                raise "Invalid Monitor Id #{monitor_id} in get_parent_monitor_id"
            end
        end

        def get_parent_monitor_labels(monitor_id, monitor_labels, parent_monitor_id)
            labels_to_copy = @health_model_definition[monitor_id]['labels']
            if labels_to_copy.nil?
                return {}
            end
            parent_monitor_labels = {}
            labels_to_copy.each{|label|
                parent_monitor_labels[label] = monitor_labels[label]
            }
            return parent_monitor_labels
        end

        def get_parent_monitor_config(parent_monitor_id)
            return @health_model_definition[parent_monitor_id]
        end

        def get_parent_monitor_instance_id(parent_monitor_id, parent_monitor_labels)
            labels = AggregateMonitorInstanceIdLabels.get_labels_for(parent_monitor_id)
            if !labels.is_a?(Array)
                raise "Expected #{labels} to be an Array for #{parent_monitor_id}"
            end
            values = labels.map{|label| parent_monitor_labels[label]}
            if values.nil? || values.empty? || values.size == 0
                return parent_monitor_id
            end
            return "#{parent_monitor_id}-#{values.join('-')}"
        end
    end
end